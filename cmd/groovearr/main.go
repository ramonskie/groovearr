package main

import (
	"os"
)

func main() {
	configPath := os.Getenv("GROOVEARR_CONFIG")
	if configPath == "" {
		configPath = "./config.json"
	}

	app, err := NewApp(configPath)
	if err != nil {
		os.Exit(1)
	}
	app.Run()
}
