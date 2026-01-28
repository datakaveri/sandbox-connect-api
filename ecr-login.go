package main

import (
	"encoding/base64"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecr"
)

var (
	AWSAccessKeyID     = ""
	AWSSecretAccessKey = ""
	AWSRegion          = ""
)

func main() {
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String(AWSRegion),
		Credentials: credentials.NewStaticCredentials(AWSAccessKeyID, AWSSecretAccessKey, ""),
	})
	if err != nil {
		log.Fatalf("Error creating session: %v", err)
	}

	svc := ecr.New(sess)

	result, err := svc.GetAuthorizationToken(&ecr.GetAuthorizationTokenInput{})
	if err != nil {
		log.Fatalf("Error getting authorization token: %v", err)
	}

	if len(result.AuthorizationData) == 0 {
		log.Fatal("No authorization data returned")
	}

	authToken := *result.AuthorizationData[0].AuthorizationToken
	decodedToken, _ := base64.StdEncoding.DecodeString(authToken)

	fmt.Println(string(decodedToken))
}
