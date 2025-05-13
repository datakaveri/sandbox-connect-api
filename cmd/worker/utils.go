package main

func getTemplateKey(template string) string {

	if template == "ai" {
		return "template/ai.zip"
	} else {
		return "template/ml.zip"
	}
}
