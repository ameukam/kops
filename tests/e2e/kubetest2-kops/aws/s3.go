/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"k8s.io/klog/v2"
)

// It contains S3Client, an Amazon S3 service client that is used to perform bucket
// and object actions.
type awsClient struct {
	S3Client *s3.Client
}

func AWSBucketName(ctx context.Context) (string, error) {
	config, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	stsSvc := sts.NewFromConfig(config)
	callerIdentity, err := stsSvc.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("building AWS STS presigned request: %w", err)
	}
	bucket := "k8s-infra-kops-" + *aws.String(*callerIdentity.Account)

	return bucket, nil
}

func (client awsClient) EnsureS3Bucket(ctx context.Context, bucketName string) error {
	_, err := client.S3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraintUsEast2,
		},
	})

	var exists *types.BucketAlreadyExists
	if err != nil {
		if errors.As(err, &exists) {
			klog.Infof("Bucket %s already exists.\n", bucketName)
			err = exists
		}
	} else {
		err := s3.NewBucketExistsWaiter(client.S3Client).Wait(
			ctx, &s3.HeadBucketInput{
				Bucket: aws.String(bucketName),
			},
			time.Minute)
		if err != nil {
			klog.Infof("Failed attempt to wait for bucket %s to exist.", bucketName)
		}
	}
	return err
}

func (client awsClient) DeleteS3Bucket(ctx context.Context, bucketName string) error {
	_, err := client.S3Client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		var noBucket *types.NoSuchBucket
		if errors.As(err, &noBucket) {
			klog.Infof("Bucket %s does not exits", bucketName)
			err = noBucket
		} else {
			klog.Infof("Couldn't delete bucket %s. Reason: %v", bucketName, err)
		}
	} else {
		err = s3.NewBucketNotExistsWaiter(client.S3Client).Wait(
			ctx, &s3.HeadBucketInput{
				Bucket: aws.String(bucketName),
			},
			time.Minute)
		if err != nil {
			klog.Infof("Failed attempt to wait for bucket %s to be deleted", bucketName)
		} else {
			klog.Infof("Bucket %s deleted", bucketName)
		}
	}
	return err
}
