package ffuf

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type AutocalibrationStrategy map[string][]string

func (j *Job) autoCalibrationStrings() map[string][]string {
	rand.Seed(time.Now().UnixNano())
	cInputs := make(map[string][]string)

	if len(j.Config.AutoCalibrationStrings) > 0 {
		cInputs["custom"] = append(cInputs["custom"], j.Config.AutoCalibrationStrings...)
		return cInputs

	}

	for _, strategy := range j.Config.AutoCalibrationStrategies {
		jsonStrategy, err := os.ReadFile(filepath.Join(AUTOCALIBDIR, strategy+".json"))
		if err != nil {
			j.Output.Warning(fmt.Sprintf("Skipping strategy \"%s\" because of error: %s\n", strategy, err))
			continue
		}

		tmpStrategy := AutocalibrationStrategy{}
		err = json.Unmarshal(jsonStrategy, &tmpStrategy)
		if err != nil {
			j.Output.Warning(fmt.Sprintf("Skipping strategy \"%s\" because of error: %s\n", strategy, err))
			continue
		}

		cInputs = mergeMaps(cInputs, tmpStrategy)
	}

	return cInputs
}



func (j *Job) shouldSkipURL(host string) bool {
    if j.errorTracker == nil {
        j.errorTracker = make(map[string]*ErrorTracker)
    }
    
    tracker, exists := j.errorTracker[host]
    if !exists {
        j.errorTracker[host] = &ErrorTracker{
            ErrorCount: 0,
            MaxRetries: 50,
        }
        return false
    }
    
    return tracker.ErrorCount >= tracker.MaxRetries
}


func (j *Job) isDuplicateResponse(resp Response) bool {
    if j.seenResponses == nil {
        j.seenResponses = make(map[ResponseSignature][]Response)
    }

    // First check if we have any responses with the same status code
    for _, seenResp := range j.seenResponses[ResponseSignature{StatusCode: resp.StatusCode}] {
        if seenResp.StatusCode != resp.StatusCode {
            continue
        }

        // Count how many attributes match
        matchCount := 0

        // Compare Size
        if seenResp.ContentLength == resp.ContentLength {
            matchCount++
        }

        // Compare Words
        if seenResp.ContentWords == resp.ContentWords {
            matchCount++
        }

        // Compare Lines
        if seenResp.Lines == resp.Lines {
            matchCount++
        }

        // If status code is same AND at least 2 other attributes match, consider it duplicate
        if matchCount >= 2 {
            return true
        }
    }

    // If not duplicate, add to seen responses
    signature := ResponseSignature{StatusCode: resp.StatusCode}
    j.seenResponses[signature] = append(j.seenResponses[signature], resp)
    return false
}




func setupDefaultAutocalibrationStrategies() error {
	basic_strategy := AutocalibrationStrategy{
		"basic_admin":  []string{"admin" + RandomString(16), "admin" + RandomString(8)},
		"htaccess":     []string{".htaccess" + RandomString(16), ".htaccess" + RandomString(8)},
		"basic_random": []string{RandomString(16), RandomString(8)},
	}
	basic_strategy_json, err := json.Marshal(basic_strategy)
	if err != nil {
		return err
	}

	advanced_strategy := AutocalibrationStrategy{
		"basic_admin":  []string{"admin" + RandomString(16), "admin" + RandomString(8)},
		"htaccess":     []string{".htaccess" + RandomString(16), ".htaccess" + RandomString(8)},
		"basic_random": []string{RandomString(16), RandomString(8)},
		"admin_dir":    []string{"admin" + RandomString(16) + "/", "admin" + RandomString(8) + "/"},
		"random_dir":   []string{RandomString(16) + "/", RandomString(8) + "/"},
	}
	advanced_strategy_json, err := json.Marshal(advanced_strategy)
	if err != nil {
		return err
	}

	basic_strategy_file := filepath.Join(AUTOCALIBDIR, "basic.json")
	if !FileExists(basic_strategy_file) {
		err = os.WriteFile(filepath.Join(AUTOCALIBDIR, "basic.json"), basic_strategy_json, 0640)
		return err
	}
	advanced_strategy_file := filepath.Join(AUTOCALIBDIR, "advanced.json")
	if !FileExists(advanced_strategy_file) {
		err = os.WriteFile(filepath.Join(AUTOCALIBDIR, "advanced.json"), advanced_strategy_json, 0640)
		return err
	}

	return nil
}

func (j *Job) calibrationRequest(inputs map[string][]byte) (Response, error) {
    basereq := BaseRequest(j.Config)
    req, err := j.Runner.Prepare(inputs, &basereq)
    if err != nil {
        j.Output.Error(fmt.Sprintf("Encountered an error while preparing autocalibration request: %s\n", err))
        j.incError()
        
        // Track errors for the host
        host := HostURLFromRequest(req)
        if j.errorTracker[host] == nil {
            j.errorTracker[host] = &ErrorTracker{MaxRetries: 50}
        }
        j.errorTracker[host].ErrorCount++
        
        log.Printf("%s", err)
        return Response{}, err
    }
    
    resp, err := j.Runner.Execute(&req)
    if err != nil {
        j.Output.Error(fmt.Sprintf("Encountered an error while executing autocalibration request: %s\n", err))
        j.incError()
        
        // Track errors for the host
        host := HostURLFromRequest(req)
        if j.errorTracker[host] == nil {
            j.errorTracker[host] = &ErrorTracker{MaxRetries: 50}
        }
        j.errorTracker[host].ErrorCount++
        
        log.Printf("%s", err)
        return Response{}, err
    }

    // Check if this is a duplicate response
    if j.isDuplicateResponse(resp) {
        return resp, fmt.Errorf("Duplicate response signature")
    }

    // Only calibrate on responses that would be matched otherwise
    if j.isMatch(resp) {
        return resp, nil
    }
    return resp, fmt.Errorf("Response wouldn't be matched")
}


type Response struct {
    StatusCode    int
    ContentLength int64
    ContentWords  int
    Lines        int
}

// ResponseSignature only needs StatusCode now as we're using it as primary key
type ResponseSignature struct {
    StatusCode int
}

// CalibrateForHost runs autocalibration for a specific host
func (j *Job) CalibrateForHost(host string, baseinput map[string][]byte) error {
	if j.Config.MatcherManager.CalibratedForDomain(host) {
		return nil
	}
	if baseinput[j.Config.AutoCalibrationKeyword] == nil {
		return fmt.Errorf("Autocalibration keyword \"%s\" not found in the request.", j.Config.AutoCalibrationKeyword)
	}
	cStrings := j.autoCalibrationStrings()
	input := make(map[string][]byte)
	for k, v := range baseinput {
		input[k] = v
	}
	for _, v := range cStrings {
		responses := make([]Response, 0)
		for _, cs := range v {
			input[j.Config.AutoCalibrationKeyword] = []byte(cs)
			resp, err := j.calibrationRequest(input)
			if err != nil {
				continue
			}
			responses = append(responses, resp)
			err = j.calibrateFilters(responses, true)
			if err != nil {
				j.Output.Error(fmt.Sprintf("%s", err))
			}
		}
	}
	j.Config.MatcherManager.SetCalibratedForHost(host, true)
	return nil
}

// CalibrateResponses returns slice of Responses for randomly generated filter autocalibration requests
func (j *Job) Calibrate(input map[string][]byte) error {
	if j.Config.MatcherManager.Calibrated() {
		return nil
	}
	cInputs := j.autoCalibrationStrings()

	for _, v := range cInputs {
		responses := make([]Response, 0)
		for _, cs := range v {
			input[j.Config.AutoCalibrationKeyword] = []byte(cs)
			resp, err := j.calibrationRequest(input)
			if err != nil {
				continue
			}
			responses = append(responses, resp)
		}
		_ = j.calibrateFilters(responses, false)
	}
	j.Config.MatcherManager.SetCalibrated(true)
	return nil
}

// CalibrateIfNeeded runs a self-calibration task for filtering options (if needed) by requesting random resources and
//
//	configuring the filters accordingly
func (j *Job) CalibrateIfNeeded(host string, input map[string][]byte) error {
    j.calibMutex.Lock()
    defer j.calibMutex.Unlock()
    
    // Check if we should skip this URL due to too many errors
    if j.shouldSkipURL(host) {
        return fmt.Errorf("Skipping URL due to too many errors: %s", host)
    }
    
    if !j.Config.AutoCalibration {
        return nil
    }
    if j.Config.AutoCalibrationPerHost {
        return j.CalibrateForHost(host, input)
    }
    return j.Calibrate(input)
}

func (j *Job) calibrateFilters(responses []Response, perHost bool) error {
	// Work down from the most specific common denominator
	if len(responses) > 0 {
		// Content length
		baselineSize := responses[0].ContentLength
		sizeMatch := true
		for _, r := range responses {
			if baselineSize != r.ContentLength {
				sizeMatch = false
			}
		}
		if sizeMatch {
			if perHost {
				// Check if already filtered
				for _, f := range j.Config.MatcherManager.FiltersForDomain(HostURLFromRequest(*responses[0].Request)) {
					match, _ := f.Filter(&responses[0])
					if match {
						// Already filtered
						return nil
					}
				}
				_ = j.Config.MatcherManager.AddPerDomainFilter(HostURLFromRequest(*responses[0].Request), "size", strconv.FormatInt(baselineSize, 10))
				return nil
			} else {
				// Check if already filtered
				for _, f := range j.Config.MatcherManager.GetFilters() {
					match, _ := f.Filter(&responses[0])
					if match {
						// Already filtered
						return nil
					}
				}
				_ = j.Config.MatcherManager.AddFilter("size", strconv.FormatInt(baselineSize, 10), false)
				return nil
			}
		}

		// Content words
		baselineWords := responses[0].ContentWords
		wordsMatch := true
		for _, r := range responses {
			if baselineWords != r.ContentWords {
				wordsMatch = false
			}
		}
		if wordsMatch {
			if perHost {
				// Check if already filtered
				for _, f := range j.Config.MatcherManager.FiltersForDomain(HostURLFromRequest(*responses[0].Request)) {
					match, _ := f.Filter(&responses[0])
					if match {
						// Already filtered
						return nil
					}
				}
				_ = j.Config.MatcherManager.AddPerDomainFilter(HostURLFromRequest(*responses[0].Request), "word", strconv.FormatInt(baselineWords, 10))
				return nil
			} else {
				// Check if already filtered
				for _, f := range j.Config.MatcherManager.GetFilters() {
					match, _ := f.Filter(&responses[0])
					if match {
						// Already filtered
						return nil
					}
				}
				_ = j.Config.MatcherManager.AddFilter("word", strconv.FormatInt(baselineWords, 10), false)
				return nil
			}
		}

		// Content lines
		baselineLines := responses[0].ContentLines
		linesMatch := true
		for _, r := range responses {
			if baselineLines != r.ContentLines {
				linesMatch = false
			}
		}
		if linesMatch {
			if perHost {
				// Check if already filtered
				for _, f := range j.Config.MatcherManager.FiltersForDomain(HostURLFromRequest(*responses[0].Request)) {
					match, _ := f.Filter(&responses[0])
					if match {
						// Already filtered
						return nil
					}
				}
				_ = j.Config.MatcherManager.AddPerDomainFilter(HostURLFromRequest(*responses[0].Request), "line", strconv.FormatInt(baselineLines, 10))
				return nil
			} else {
				// Check if already filtered
				for _, f := range j.Config.MatcherManager.GetFilters() {
					match, _ := f.Filter(&responses[0])
					if match {
						// Already filtered
						return nil
					}
				}
				_ = j.Config.MatcherManager.AddFilter("line", strconv.FormatInt(baselineLines, 10), false)
				return nil
			}
		}
	}
	return fmt.Errorf("No common filtering values found")
}
