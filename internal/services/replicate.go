package services

import (
	"context"
	"fmt"
	"log"
	"strings"

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

// AWAL PERUBAHAN
func (c *ReplicateClient) CreatePrediction(ctx context.Context, modelID, prompt string, imageURL string, imageParamName string, aspectRatio string, numOutputs int, customParams map[string]interface{}, imageURLs ...[]string) ([]string, error) {
	// 1. Inisialisasi input
	input := replicate.PredictionInput{}

	// 2. Masukkan customParams TERLEBIH DAHULU (sebagai nilai default/tambahan)
	// Dengan begini, jika nanti parameter eksplisit (seperti num_outputs) dimasukkan, 
	// parameter eksplisit itu yang akan menimpa (menang), bukan sebaliknya.
	for key, value := range customParams {
		if value != nil {
			log.Printf("INFO: Applying custom parameter '%s' with value '%v'", key, value)
			input[key] = value
		}
	}

	// 3. Masukkan Parameter Eksplisit (Ini yang akan digunakan API jika terjadi duplikasi key)
	input["prompt"] = prompt

	paramName := "input_image"
	if imageParamName != "" {
		paramName = imageParamName
	}

	if len(imageURLs) > 0 && len(imageURLs[0]) > 0 {
		input[paramName] = imageURLs[0]
	} else if imageURL != "" {
		input[paramName] = imageURL
	}

	if aspectRatio != "" {
		input["aspect_ratio"] = aspectRatio
	}
	
	// FIX BUG: Ubah kondisi dari '> 1' menjadi '> 0'.
	// Jika user minta 1 gambar, kita HARUS kirim "num_outputs": 1 secara eksplisit.
	// Jika tidak dikirim, API akan memakai default model (bisa jadi 4).
	if numOutputs > 0 {
		input["num_outputs"] = numOutputs
	}

	// Debug log untuk melihat apa yang sebenarnya dikirim
	log.Printf("DEBUG: Final Prediction Input for %s: %+v", modelID, input)

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

func (c *ReplicateClient) CreateTextCompletion(ctx context.Context, modelID string, prompt string, systemInstruction string, temperature float64, maxOutputTokens int, thinkingBudget int) (string, error) {
	input := replicate.PredictionInput{
		"prompt": prompt,
	}

	if systemInstruction != "" {
		input["system_instruction"] = systemInstruction
	}
	
	// Default temperature Gemini biasanya 1.0, kita izinkan custom
	if temperature > 0 {
		input["temperature"] = temperature
	}

	if maxOutputTokens > 0 {
		input["max_output_tokens"] = maxOutputTokens
	}

	// [BARU] Set Thinking Budget (sesuai request)
	if thinkingBudget > 0 {
		input["thinking_budget"] = thinkingBudget
	}

	log.Printf("DEBUG: Sending text request to %s. Prompt len: %d", modelID, len(prompt))

	// Jalankan prediksi
	output, err := c.client.Run(ctx, modelID, input, nil)
	if err != nil {
		log.Printf("ERROR: Text generation failed: %v", err)
		return "", err
	}

	// Parsing Output Gemini (biasanya array of strings yang perlu digabung)
	if outputSlice, ok := output.([]interface{}); ok {
		var sb strings.Builder
		for _, item := range outputSlice {
			if str, ok := item.(string); ok {
				sb.WriteString(str)
			}
		}
		return sb.String(), nil
	}
	
	// Jika output langsung string
	if outputStr, ok := output.(string); ok {
		return outputStr, nil
	}

	return "", fmt.Errorf("unknown output format from LLM")
}

// [BARU] Fungsi khusus untuk Vision (Gambar + Teks -> Teks)
// [PERBAIKAN] Fungsi Vision dengan parameter 'images' (Array)
func (c *ReplicateClient) CreateVisionCompletion(ctx context.Context, modelID string, prompt string, imageURL string, maxOutputTokens int) (string, error) {
	// Masukkan imageURL ke dalam slice/array string
	input := replicate.PredictionInput{
		"prompt": prompt,
		"images": []string{imageURL}, // <-- PERBAIKAN: Gunakan 'images' dan format array
	}

	if maxOutputTokens > 0 {
		input["max_output_tokens"] = maxOutputTokens
	}

	log.Printf("DEBUG: Sending VISION request to %s with image: %s", modelID, imageURL)

	output, err := c.client.Run(ctx, modelID, input, nil)
	if err != nil {
		log.Printf("ERROR: Vision generation failed: %v", err)
		return "", err
	}

	// Parsing Output
	if outputSlice, ok := output.([]interface{}); ok {
		var sb strings.Builder
		for _, item := range outputSlice {
			if str, ok := item.(string); ok {
				sb.WriteString(str)
			}
		}
		return sb.String(), nil
	}

	if outputStr, ok := output.(string); ok {
		return outputStr, nil
	}

	return "", fmt.Errorf("unknown output format from Vision Model")
}