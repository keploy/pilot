package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.keploy.io/server/cmd"
	"go.keploy.io/server/pkg/models"
	"go.keploy.io/server/utils"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewDevelopmentLogger creates and returns a new zap logger for development use.
// It takes a log level as a string (e.g., "debug", "info") and sets up the logger accordingly.
func NewDevelopmentLogger(level string) (*zap.Logger, error) {
	cfg := zap.NewDevelopmentConfig()

	// Parse the string level to zap's atomic level
	var lvl zapcore.Level
	err := lvl.UnmarshalText([]byte(strings.ToLower(level)))
	if err != nil {
		return nil, err
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)

	// Build and return the logger
	logger, err := cfg.Build()
	if err != nil {
		return nil, err
	}

	return logger, nil
}

func CheckFileExists(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}
	return true
}

func GetNoiseFromConfig(configPath string) (models.GlobalNoise, models.TestsetNoise, error) {
	globalNoise := make(models.GlobalNoise)
	testSetNoise := make(models.TestsetNoise)

	configFilePath := filepath.Join(configPath, "keploy-config.yaml")
	if isExist := CheckFileExists(configFilePath); !isExist {
		return globalNoise, testSetNoise, fmt.Errorf("config file does not exist at path: %s", configFilePath)
	}
	confTest, err := cmd.ReadTestConfig(configFilePath)
	if err != nil {
		confErr := fmt.Errorf("failed to read test config from the config file due to error: %s", err)
		fmt.Println("You have probably edited the config file incorrectly. Please follow the guide below.")
		fmt.Println(utils.ConfigGuide)
		return globalNoise, testSetNoise, confErr
	}

	globalNoise = confTest.GlobalNoise.Global
	testSetNoise = confTest.GlobalNoise.Testsets

	return globalNoise, testSetNoise, nil
}

func compareSessions(session1, session2 []string, logger *zap.Logger) bool {
	if len(session1) != len(session2) {
		logger.Error("number of sessions in both test sets are not equal", zap.Int("pre-recorded", len(session1)), zap.Int("test-bench", len(session2)))
		return false
	}

	sort.Strings(session1)
	sort.Strings(session2)

	for i := 0; i < len(session1); i++ {
		if session1[i] != session2[i] {
			logger.Error("session names are not equal", zap.String("pre-recorded", session1[i]), zap.String("test-bench", session2[i]))
			return false
		}
	}
	return true
}

func swapFiles(file1Path, file2Path string) error {
	// Read content from the first file
	content1, err := ioutil.ReadFile(file1Path)
	if err != nil {
		return err
	}

	// Create a temporary file
	tempFile, err := ioutil.TempFile("", "temp")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name()) // Clean up

	// Write the content of the first file to the temporary file
	if _, err := tempFile.Write(content1); err != nil {
		tempFile.Close()
		return err
	}
	tempFile.Close()

	// Read content from the second file
	content2, err := ioutil.ReadFile(file2Path)
	if err != nil {
		return err
	}

	// Write the content of the second file to the first file
	if err := ioutil.WriteFile(file1Path, content2, 0777); err != nil {
		return err
	}

	// Write the content of the temporary file to the second file
	if err := ioutil.WriteFile(file2Path, content1, 0777); err != nil {
		return err
	}

	return nil
}

func getAbsolutePath(path string) (string, error) {
	//if user provides relative path
	if len(path) > 0 && path[0] != '/' {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("failed to get the absolute path from relative path:%v", err)
		}
		path = absPath
	} else if len(path) == 0 { // if user doesn't provide any path
		cdirPath, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get the path of current directory:%v", err)
		}
		path = cdirPath
	} else {
		// user provided the absolute path
	}
	return path, nil
}
