package s3

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Client struct {
	Client *s3.Client
}

func NewS3Client(accessKey, secretKey, s3Region string) (*S3Client, error) {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(s3Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, err
	}

	return &S3Client{Client: s3.NewFromConfig(cfg)}, nil
}

func (client *S3Client) GetPresignedUrl(bucketName, key string) (*string, error) {
	presignClient := s3.NewPresignClient(client.Client)
	req, err := presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &bucketName,
		Key:    &key,
	}, s3.WithPresignExpires(time.Hour*24))
	if err != nil {
		return nil, err
	}

	return &req.URL, nil
}
