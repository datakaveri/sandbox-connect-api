package utils

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/go-playground/validator/v10"
)

func LogErrorAndExit(logger *slog.Logger, msg string, args ...any) {
	logger.Error(msg, args...)
	os.Exit(1)
}
func DecodeAndValidate[T any](reqBody io.Reader, logger *slog.Logger) (T, error) {
	var body T
	err := json.NewDecoder(reqBody).Decode(&body)
	if err != nil {
		logger.Error("Can't parse the body", "Error", err.Error())
		return body, err
	}
	validate := validator.New()
	if err = validate.Struct(body); err != nil {
		logger.Error("Can't validate the body", "Error", err.Error())
		return body, err
	}
	return body, nil
}
