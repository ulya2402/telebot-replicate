package services

import (
	"context"
	"fmt"
	"log"

	"github.com/replicate/replicate-go"
)

type ReplicateClient struct {
	client *replicate.Client
}

func NewReplicateClient(apiToken string) (*ReplicateClient, error) {
	r8, err := replicate.NewClient(replicate.WithToken(apiToken))
	if err != nil {
		log.Fatalf("FATAL: Failed to create replicate client: %v", err)
		return nil, err
	}
	return &ReplicateClient{client: r8}, nil
}

func (c *ReplicateClient) CreatePrediction(ctx context.Context, modelID, prompt string, imageURL string, imageParamName string, aspectRatio string, numOutputs int, customParams map[string]interface{}) ([]string, error) {
	input := replicate.PredictionInput{
		"prompt": prompt,
	}

	// Tambahkan parameter opsional ke input jika nilainya ada
	if imageURL != "" {
		// Gunakan imageParamName jika ada, jika tidak gunakan default "input_image"
		paramName := "input_image"
		if imageParamName != "" {
			paramName = imageParamName
		}
		input[paramName] = imageURL
	}

	if aspectRatio != "" {
		input["aspect_ratio"] = aspectRatio
	}
	if numOutputs > 1 {
		input["num_outputs"] = numOutputs
	}

	for key, value := range customParams {
		if value != nil {
			log.Printf("INFO: Applying custom parameter '%s' with value '%v'", key, value)
			input[key] = value
		}
	}

	if customParams != nil {
		for key, value := range customParams {
			if value != nil {
				log.Printf("INFO: Applying custom parameter '%s' with value '%v'", key, value)
				input[key] = value
			}
		}
	}

	output, err := c.client.Run(ctx, modelID, input, nil)
	if err != nil {
		log.Printf("ERROR: Prediction failed to complete: %v", err)
		return nil, err
	}

	var urls []string
	if outputSlice, ok := output.([]interface{}); ok {
		for _, item := range outputSlice {
			if url, ok := item.(string); ok {
				urls = append(urls, url)
			}
		}
		return urls, nil
	}

	if outputString, ok := output.(string); ok {
		urls = append(urls, outputString)
		return urls, nil
	}

	err = fmt.Errorf("prediction output is in an unknown format")
	log.Printf("ERROR: %v", err)
	return nil, err
}