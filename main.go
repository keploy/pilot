package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.keploy.io/server/v2/pkg"
	testdb "go.keploy.io/server/v2/pkg/platform/yaml/testdb"
	"go.keploy.io/server/v2/pkg/service/replay"
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
		panic("failed to create logger")
	}
	syncErr := logger.Sync()
	if syncErr != nil {
		logger.Debug("failed to sync logger", zap.Error(syncErr))
	}

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
	println("ConfigPath:", *configPath)
	// get all the sessions
	tsets1, err := pkg.ReadSessionIndices(*preRecPath, logger)
	if err != nil {
		logger.Error("failed to read session indices", zap.String("path", *preRecPath), zap.Error(err))
		return
	}

	tsets2, err := pkg.ReadSessionIndices(*testBenchPath, logger)
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

	if len(sessions) == 0 {
		logger.Info("no sessions found")
		return
	}

	// initialize the test dbs
	db1 := testdb.New(logger, *preRecPath)
	db2 := testdb.New(logger, *testBenchPath)

	//TODO: handle ctx cancel
	ctx := context.Background()

	if *testAssert {
		// Run the test assertions
		ok := compareTestCases(ctx, logger, db1, db2, sessions, *configPath)
		if !ok {
			logger.Error("test cases are not equal")
			os.Exit(1)
		} else {
			logger.Info("test cases are equal")
			os.Exit(0)
		}
	} else if *mockAssert {
		// Prepare the mock assertions
		ok := prepareMockAssertion(ctx, logger, db1, db2, sessions, *preRecPath, *testBenchPath)
		if !ok {
			logger.Error("failed to prepare mock assertions")
			os.Exit(1)
		} else {
			logger.Info("mock assertions are prepared")
			os.Exit(0)
		}
	}

}

func compareTestCases(ctx context.Context, logger *zap.Logger, db1, db2 *testdb.TestYaml, sessions []string, configPath string) bool {
	// get noise from config
	noise, err := GetNoiseFromConfig(logger, configPath)
	if err != nil {
		logger.Info("failed to get noise from config, continuing without config file", zap.Error(err))
	}

	passedOverall := true

	for _, session := range sessions {
		fmt.Println("Session:", session)

		noiseConfig := noise.Global
		if tsNoise, ok := noise.Testsets[session]; ok {
			noiseConfig = replay.LeftJoinNoise(noise.Global, tsNoise)
		}

		// get all the test cases of a session from both pre-recorded and test-bench

		// read test cases from pre-recorded
		readTcs1, err := db1.GetTestCases(ctx, session)
		if err != nil {
			logger.Error("failed to get test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		// Sort in ascending order of test case name
		sort.Slice(readTcs1, func(i, j int) bool {
			return readTcs1[i].Name < readTcs1[j].Name
		})

		// read test cases from test-bench
		readTcs2, err := db2.GetTestCases(ctx, session)
		if err != nil {
			logger.Error("failed to get test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		sort.Slice(readTcs2, func(i, j int) bool {
			return readTcs2[i].Name < readTcs2[j].Name
		})

		if len(readTcs1) != len(readTcs2) {
			logger.Error("number of test cases in both test sets are not equal", zap.Int("pre-recorded", len(readTcs1)), zap.Int("test-bench", len(readTcs2)))
			return false
		}

		// Do absolute matching of test cases
		testSetRes := true
		for i := 0; i < len(readTcs1); i++ {
			ok, req, resp, absRes := replay.AbsMatch(readTcs1[i], readTcs2[i], noiseConfig, true, logger)
			if !ok {
				logger.Error("Tests are different", zap.String("Pre-recorded", readTcs1[i].Name), zap.String("Test-bench", readTcs2[i].Name))
				if !req {
					fmt.Printf("HttpReq diff:%v\n", absRes.Req)
				}
				if !resp {
					fmt.Printf("HttpResp diff:%v\n", absRes.Resp)
				}
			}
			testSetRes = testSetRes && ok
		}
		passedOverall = passedOverall && testSetRes
	}

	return passedOverall
}

func prepareMockAssertion(ctx context.Context, logger *zap.Logger, db1, db2 *testdb.TestYaml, sessions []string, preRecPath, testBenchPath string) bool {

	for _, session := range sessions {
		// get all the test cases of a session from both pre-recorded and test-bench

		// read test cases from pre-recorded
		readTcs1, err := db1.GetTestCases(ctx, session)
		if err != nil {
			logger.Error("failed to get test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		//Sort in ascending order of test case name
		sort.Slice(readTcs1, func(i, j int) bool {
			return readTcs1[i].Name < readTcs1[j].Name
		})

		// read test cases from test-bench
		readTcs2, err := db2.GetTestCases(ctx, session)
		if err != nil {
			logger.Error("failed to get test cases", zap.String("session", session), zap.Error(err))
			return false
		}

		sort.Slice(readTcs2, func(i, j int) bool {
			return readTcs2[i].Name < readTcs2[j].Name
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
			req1Time := readTcs1[i].HTTPReq.Timestamp
			req2Time := readTcs2[i].HTTPReq.Timestamp
			logger.Debug("Before swapping request timestamps", zap.Time("pre-recorded", req1Time), zap.Time("test-bench", req2Time))

			readTcs1[i].HTTPReq.Timestamp = req2Time
			readTcs2[i].HTTPReq.Timestamp = req1Time
			logger.Debug("After swapping request timestamps", zap.Time("pre-recorded", readTcs1[i].HTTPReq.Timestamp), zap.Time("test-bench", readTcs2[i].HTTPReq.Timestamp))

			//swap response timestamps
			res1Time := readTcs1[i].HTTPResp.Timestamp
			res2Time := readTcs2[i].HTTPResp.Timestamp
			logger.Debug("Before swapping response timestamps", zap.Time("pre-recorded", res1Time), zap.Time("test-bench", res2Time))

			readTcs1[i].HTTPResp.Timestamp = res2Time
			readTcs2[i].HTTPResp.Timestamp = res1Time
			logger.Debug("After swapping response timestamps", zap.Time("pre-recorded", readTcs1[i].HTTPResp.Timestamp), zap.Time("test-bench", readTcs2[i].HTTPResp.Timestamp))

			//update both the test cases
			err = db1.UpdateTestCase(ctx, readTcs1[i], session)
			if err != nil {
				logger.Error("failed to update test case", zap.String("Path", preRecPath+"/"+session+"/tests"), zap.String("test case name", readTcs1[i].Name), zap.Error(err))
				return false
			}
			err = db2.UpdateTestCase(ctx, readTcs2[i], session)
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
