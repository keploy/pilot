package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.keploy.io/server/pkg/models"
	kYaml "go.keploy.io/server/pkg/platform/yaml"
	"go.keploy.io/server/pkg/service/test"
	_ "go.keploy.io/server/pkg/service/test"
	"go.uber.org/zap"
)

func main() {

	// Define the flags
	testAssert := flag.Bool("test-assert", false, "Enable test assertions")
	mockAssert := flag.Bool("mock-assert", false, "Enable mock assertion preparation")

	preRecPath := flag.String("preRecPath", ".", "Path to pre-recorded test cases & mocks")
	testBenchPath := flag.String("testBenchPath", ".", "Path to testbench test cases & mocks")
	configPath := flag.String("configPath", ".", "Path to configuration file")

	// Parse the flags
	flag.Parse()

	if *testAssert && *mockAssert {
		panic("Please provide either -test-assert or -mock-assert flag")
	}

	if !*testAssert && !*mockAssert {
		panic("Please provide either -test-assert or -mock-assert flag")
	}

	// Create a new zap logger (info, debug, warn, error, fatal, panic)
	logger, err := NewDevelopmentLogger("info")
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	//Make paths absolute
	*preRecPath, err = getAbsolutePath(*preRecPath)
	if err != nil {
		logger.Error("failed to get absolute path", zap.String("path", *preRecPath), zap.Error(err))
		return
	}
	*preRecPath = filepath.Join(*preRecPath, "keploy")

	*testBenchPath, err = getAbsolutePath(*testBenchPath)
	if err != nil {
		logger.Error("failed to get absolute path", zap.String("path", *testBenchPath), zap.Error(err))
		return
	}
	*testBenchPath = filepath.Join(*testBenchPath, "keploy")

	*configPath, err = getAbsolutePath(*configPath)
	if err != nil {
		logger.Error("failed to get absolute path", zap.String("path", *configPath), zap.Error(err))
		return
	}

	ys := kYaml.NewYamlStore("", "", "", "", logger, nil)

	// get all the sessions
	tsets1, err := kYaml.ReadSessionIndices(*preRecPath, logger)
	if err != nil {
		logger.Error("failed to read session indices", zap.String("path", *preRecPath), zap.Error(err))
		return
	}

	tsets2, err := kYaml.ReadSessionIndices(*testBenchPath, logger)
	if err != nil {
		logger.Error("failed to read session indices", zap.String("path", *testBenchPath), zap.Error(err))
		return
	}

	// compare sessions, both should contain equal number of same sessions
	ok := compareSessions(tsets1, tsets2, logger)
	if !ok {
		logger.Error("sessions are not equal")
		return
	}
	sessions := tsets1

	if *testAssert {
		// Run the test assertions
		ok := compareTestCases(ys, sessions, *preRecPath, *testBenchPath, *configPath, logger)
		if !ok {
			logger.Error("test cases are not equal")
			os.Exit(1)
		} else {
			logger.Info("test cases are equal")
			os.Exit(0)
		}
	} else if *mockAssert {
		// Prepare the mock assertions
		ok := PrepareMockAssertion(ys, sessions, *preRecPath, *testBenchPath, logger)
		if !ok {
			logger.Error("failed to prepare mock assertions")
			os.Exit(1)
		} else {
			logger.Info("mock assertions are prepared")
			os.Exit(0)
		}
	}

	return
}

// compareTestCases compares the test cases of pre-recorded and test-bench
func compareTestCases(ys *kYaml.Yaml, sessions []string, preRecPath, testBenchPath, configPath string, logger *zap.Logger) bool {
	if len(sessions) == 0 {
		logger.Error("no sessions found")
		return false
	}

	// get noise from config
	globalNoise, testSetNoise, err := GetNoiseFromConfig(configPath)
	if err != nil {
		logger.Info("failed to get noise from config, continuing without config file", zap.Error(err))
		// return false
	}

	passedOverall := true

	for _, session := range sessions {

		noiseConfig := globalNoise
		if tsNoise, ok := testSetNoise[session]; ok {
			noiseConfig = test.LeftJoinNoise(globalNoise, tsNoise)
		}

		// get all the test cases of a session from both pre-recorded and test-bench

		// read test cases from pre-recorded
		var readTcs1 []*models.TestCase
		tcs1, err := ys.ReadTestcase(filepath.Join(preRecPath, session, "tests"), nil, nil)
		if err != nil {
			logger.Error("failed to read test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		for _, kind := range tcs1 {
			tcs, ok := kind.(*models.TestCase)
			if !ok {
				continue
			}
			readTcs1 = append(readTcs1, tcs)
		}

		//Sort in ascending order of test case name
		sort.Slice(readTcs1, func(i, j int) bool {
			return readTcs1[i].Name < readTcs1[j].Name
		})

		// read test cases from test-bench
		var readTcs2 []*models.TestCase
		tcs2, err := ys.ReadTestcase(filepath.Join(testBenchPath, session, "tests"), nil, nil)
		if err != nil {
			logger.Error("failed to read test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		for _, kind := range tcs2 {
			tcs, ok := kind.(*models.TestCase)
			if !ok {
				continue
			}
			readTcs2 = append(readTcs2, tcs)
		}

		//Sort in ascending order of test case name getting in "Keploy-Test-Id" header
		sort.Slice(readTcs2, func(i, j int) bool {
			return readTcs2[i].HttpReq.Header["Keploy-Test-Id"] < readTcs2[j].HttpReq.Header["Keploy-Test-Id"]
		})

		if len(readTcs1) != len(readTcs2) {
			logger.Error("number of test cases in both test sets are not equal", zap.Int("pre-recorded", len(readTcs1)), zap.Int("test-bench", len(readTcs2)))
			return false
		}

		// Do absolute matching of test cases
		testSetRes := true
		for i := 0; i < len(readTcs1); i++ {
			ok, res := test.AbsMatch(readTcs1[i], readTcs2[i], noiseConfig, logger)
			if !ok {
				logger.Error("Tests are different", zap.String("Pre-recorded", readTcs1[i].Name), zap.String("Test-bench", readTcs2[i].Name))
				fmt.Printf("HttpReq diff:%v\n\n\n", res.ReqResult)
				fmt.Printf("HttpResp diff:%v\n", res.RespResult)
			}
			testSetRes = testSetRes && ok
		}
		passedOverall = passedOverall && testSetRes
	}

	return passedOverall
}

// PrepareMockAssertion prepares the mock assertions by swapping the mock files of pre-recorded and test-bench
// and swapping the timestamps of test cases of pre-recorded and test-bench
func PrepareMockAssertion(ys *kYaml.Yaml, sessions []string, preRecPath, testBenchPath string, logger *zap.Logger) bool {
	if len(sessions) == 0 {
		logger.Error("no sessions found")
		return false
	}

	for _, session := range sessions {
		// get all the test cases of a session from both pre-recorded and test-bench

		// read test cases from pre-recorded
		var readTcs1 []*models.TestCase
		tcs1, err := ys.ReadTestcase(filepath.Join(preRecPath, session, "tests"), nil, nil)
		if err != nil {
			logger.Error("failed to read test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		for _, kind := range tcs1 {
			tcs, ok := kind.(*models.TestCase)
			if !ok {
				continue
			}
			readTcs1 = append(readTcs1, tcs)
		}

		//Sort in ascending order of test case name
		sort.Slice(readTcs1, func(i, j int) bool {
			return readTcs1[i].Name < readTcs1[j].Name
		})

		// read test cases from test-bench
		var readTcs2 []*models.TestCase
		tcs2, err := ys.ReadTestcase(filepath.Join(testBenchPath, session, "tests"), nil, nil)
		if err != nil {
			logger.Error("failed to read test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		for _, kind := range tcs2 {
			tcs, ok := kind.(*models.TestCase)
			if !ok {
				continue
			}
			readTcs2 = append(readTcs2, tcs)
		}

		//Sort in ascending order of test case name getting in "Keploy-Test-Id" header
		sort.Slice(readTcs2, func(i, j int) bool {
			return readTcs2[i].HttpReq.Header["Keploy-Test-Id"] < readTcs2[j].HttpReq.Header["Keploy-Test-Id"]
		})

		if len(readTcs1) != len(readTcs2) {
			logger.Error("number of test cases in both test sets are not equal", zap.Int("pre-recorded", len(readTcs1)), zap.Int("test-bench", len(readTcs2)))
			return false
		}

		//Swap test timestamps of both request and response
		for i := 0; i < len(readTcs1); i++ {
			if readTcs1[i].Name != readTcs2[i].Name {
				logger.Error("test case names are not equal", zap.String("pre-recorded", readTcs1[i].Name), zap.String("test-bench", readTcs2[i].Name))
				return false
			}
			//swap request timestamps
			req1Time := readTcs1[i].HttpReq.Timestamp
			req2Time := readTcs2[i].HttpReq.Timestamp
			logger.Debug("Before swapping request timestamps", zap.Time("pre-recorded", req1Time), zap.Time("test-bench", req2Time))

			readTcs1[i].HttpReq.Timestamp = req2Time
			readTcs2[i].HttpReq.Timestamp = req1Time
			logger.Debug("After swapping request timestamps", zap.Time("pre-recorded", readTcs1[i].HttpReq.Timestamp), zap.Time("test-bench", readTcs2[i].HttpReq.Timestamp))

			//swap response timestamps
			res1Time := readTcs1[i].HttpResp.Timestamp
			res2Time := readTcs2[i].HttpResp.Timestamp
			logger.Debug("Before swapping response timestamps", zap.Time("pre-recorded", res1Time), zap.Time("test-bench", res2Time))

			readTcs1[i].HttpResp.Timestamp = res2Time
			readTcs2[i].HttpResp.Timestamp = res1Time
			logger.Debug("After swapping response timestamps", zap.Time("pre-recorded", readTcs1[i].HttpResp.Timestamp), zap.Time("test-bench", readTcs2[i].HttpResp.Timestamp))

			//update both the test cases
			err = ys.UpdateTestCase(readTcs1[i], filepath.Join(preRecPath, session, "tests"), readTcs1[i].Name, context.Background())
			if err != nil {
				logger.Error("failed to update test case", zap.String("Path", preRecPath+"/"+session+"/tests"), zap.String("test case name", readTcs1[i].Name), zap.Error(err))
				return false
			}
			err = ys.UpdateTestCase(readTcs2[i], filepath.Join(testBenchPath, session, "tests"), readTcs2[i].Name, context.Background())
			if err != nil {
				logger.Error("failed to update test case", zap.String("Path", testBenchPath+"/"+session+"/tests"), zap.String("test case name", readTcs2[i].Name), zap.Error(err))
				return false
			}
		}

		//Swap mocks of both pre-recorded and test-bench
		err = swapFiles(filepath.Join(preRecPath, session, "mocks.yaml"), filepath.Join(testBenchPath, session, "mocks.yaml"))
		if err != nil {
			logger.Error("failed to swap mock files", zap.String("pre-recorded", preRecPath+"/"+session+"/mocks.yaml"), zap.String("test-bench", testBenchPath+"/"+session+"/mocks.yaml"), zap.Error(err))
			return false
		}
	}

	return true
}
