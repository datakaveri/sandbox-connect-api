package utils

import "fmt"

func GetDeductionURL(url string) string {
	return fmt.Sprintf("%s/iudx/v2/auth/admin/user/credit/deduct", url)
}

func GetBalanceURL(url string, userID string) string {
	return fmt.Sprintf("%s/iudx/v2/auth/admin/user/credit/balance/%s", url, userID)
}
