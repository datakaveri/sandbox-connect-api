package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sandbox-backend-service/pkg/utils"
	"strings"
	"time"
)

// getKeycloakToken gets a token for API calls
func (app *application) getKeycloakToken() (string, error) {
	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", app.env.BillingConfig.KeycloakClientID)
	data.Set("username", app.env.BillingConfig.KeycloakUsername)
	data.Set("password", app.env.BillingConfig.KeycloakPassword)

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		app.env.KeycloakURL, app.env.KeycloakRealm)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make token request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var tokenResponse KeycloakTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return "", fmt.Errorf("failed to decode token response: %v", err)
	}

	if tokenResponse.AccessToken == "" {
		return "", fmt.Errorf("empty access token received from Keycloak")
	}

	return tokenResponse.AccessToken, nil
}

// getUserBalance gets the current balance for a user
func (app *application) getUserBalance(userID string, token string) (float64, error) {
	balanceURL := utils.GetBalanceURL(app.env.BillingConfig.AAAURL, userID)

	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest("GET", balanceURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create balance request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to make balance request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read balance response body: %v", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("balance API returned status code: %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var response BalanceResponse
	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal balance response: %v", err)
	}

	return response.Result.Balance, nil
}

// costDeductionRequest performs a cost deduction request
func (app *application) costDeductionRequest(profileID string, cost float64, token string) (AAAResponse, error) {
	deductionURL := utils.GetDeductionURL(app.env.BillingConfig.AAAURL)

	client := &http.Client{Timeout: 5 * time.Second}

	var response AAAResponse

	// Generate timestamp
	requestTime := time.Now().Format("2006-01-02T15:04:05.000")

	// Format amount to preserve decimal places with 15 decimal precision
	amountStr := fmt.Sprintf("%.15f", cost)
	amountStr = strings.TrimRight(amountStr, "0")
	if strings.HasSuffix(amountStr, ".") {
		amountStr += "0"
	}

	payload := map[string]interface{}{
		"amount":       json.Number(amountStr),
		"user_id":      profileID,
		"requested_at": requestTime,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return response, fmt.Errorf("failed to marshal deduction payload: %v", err)
	}

	req, err := http.NewRequest("PUT", deductionURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return response, fmt.Errorf("failed to create deduction request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return response, fmt.Errorf("failed to make deduction request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return response, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return response, fmt.Errorf("AAA API returned status code: %d, response: %s", resp.StatusCode, string(responseBody))
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		return response, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	return response, nil
}

// getProfileCostFromOpenCost gets cost data from OpenCost
func (app *application) getProfileCostFromOpenCost(lastSyncAt time.Time, endTime time.Time, profile *BillingProfile) (float64, error) {
	end := endTime.Format(time.RFC3339)
	start := lastSyncAt.Format(time.RFC3339)

	openCostURL := fmt.Sprintf("%s/allocation?window=%s,%s&aggregate=namespace&format=json",
		app.env.BillingConfig.OpenCostURL, start, end)

	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodGet, openCostURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to make OpenCost request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("OpenCost returned status code: %d", resp.StatusCode)
	}

	var costData CostAllocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&costData); err != nil {
		return 0, fmt.Errorf("failed to decode cost data: %v", err)
	}

	var cost float64 = 0
	for _, dataMap := range costData.Data {
		for _, data := range dataMap {
			if data.Properties.Namespace == profile.UserID {
				cost = data.GPUCost
				break
			}
		}
	}

	return cost, nil
}
