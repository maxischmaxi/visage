package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fatih/camelcase"
	"github.com/haochi/blockhash-go"

	"github.com/chromedp/chromedp"
)

const (
	ORGANISM_TYPE_DRAFT = "draft"
	ORGANISM_TYPE_FOUNDATION = "foundation"
	ORGANISM_TYPE_ATOM = "atom"
	ORGANISM_TYPE_MOLECULE = "molecule"
	ORGANISM_TYPE_ORGANISM = "organism"
	ORGANISM_TYPE_TEMPLATE = "template"
	ORGANISM_TYPE_PAGE = "page"
)

const (
	REGRESSION_TEST_STATUS_CREATED = "created"
	REGRESSION_TEST_STATUS_FAILED = "failed"
	REGRESSION_TEST_STATUS_PASSED = "passed"
	REGRESSION_TEST_STATUS_SKIPPED = "skipped"
)

type Environment struct {
	Cwd string
	Home string
	ConfigPath string
}

type Story struct {
	Path string
	Name string
	OrganismType string
	ComponentName string
}

type RegressionTestResult struct {
	Status string
	CurrentTest RegressionTest
	ExpectedTest RegressionTest
}

type RegressionTest struct {
	Component string
	Viewport string
	DomHash string
	StyleHash string
	VisualHash string
	TimeStamp int64
}

type Config struct {
	BaseURL string `json:"base_url" bson:"base_url" yaml:"base_url"`
	RootElement string `json:"root_element" bson:"root_element" yaml:"root_element"`
	MaxThreads int `json:"max_threads" bson:"max_threads" yaml:"max_threads"`
}

func (s *Story) GetOrganismTypeString() string {
	switch s.OrganismType {
	case ORGANISM_TYPE_ATOM:
		return "atoms"
	case ORGANISM_TYPE_MOLECULE:
		return "molecules"
	case ORGANISM_TYPE_ORGANISM:
		return "organisms"
	case ORGANISM_TYPE_TEMPLATE:
		return "templates"
	case ORGANISM_TYPE_PAGE:
		return "pages"
	case ORGANISM_TYPE_FOUNDATION:
		return "foundations"
	case ORGANISM_TYPE_DRAFT:
		return "drafts"
	default:
		return "drafts"
	}
}

func (s *Story) Check(ctx context.Context, config *Config) (*RegressionTestResult, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = "iframe.html"
	name := camelcase.Split(s.Name)
	u.RawQuery = fmt.Sprintf("globals=viewport:full&args=&id=%s-%s--%s&viewMode=story",
		s.GetOrganismTypeString(),
		strings.ToLower(s.ComponentName),
		strings.ToLower(strings.Join(name, "-")),
	)

	fmt.Printf("Checking %s %s %s\n", s.ComponentName, s.Name, u.String())

	var screenshot []byte
	var domHash string
	var visualHash string
	var styleHash string
	var html string
	var style string

	id, _ := strings.CutPrefix(config.RootElement, "#")
	fmt.Println("ID:", id)
	htmlQuery := fmt.Sprintf("document.querySelector(\"#%s\").outerHTML", id)
	cssQuery := "document.styleSheets[0].cssRules[0].cssText"
	fmt.Println("HTML Query:", htmlQuery)
	fmt.Println("CSS Query:", cssQuery)

	err = chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Navigate(u.String()),
		chromedp.WaitVisible(id, chromedp.ByID),
		chromedp.WaitReady(id, chromedp.ByID),
		chromedp.Sleep(1 * time.Second),
		chromedp.Screenshot(id, &screenshot, chromedp.ByID),
		chromedp.EvaluateAsDevTools(htmlQuery, &html),
		chromedp.EvaluateAsDevTools(cssQuery, &style),
	})
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	reader := bytes.NewReader(screenshot)
	vh, err := blockhash.Blockhash(reader, 16)
	if err != nil {
		return nil, err
	}
	visualHash = vh.ToHex()

	shaHash := sha1.New()
	shaHash.Write([]byte(style))
	styleHash = string(shaHash.Sum(nil)[:])

	md5Hash := md5.New()
	md5Hash.Write([]byte(html))
	domHash = string(md5Hash.Sum(nil)[:])

	test := RegressionTest{
		Component: s.Name,
		Viewport: "full",
		DomHash: domHash,
		StyleHash: styleHash,
		VisualHash: visualHash,
	}

	result := RegressionTestResult{
		Status: REGRESSION_TEST_STATUS_CREATED,
		CurrentTest: test,
		ExpectedTest: test,
	}

	return &result, nil
}

func LoadConfig(path string) (*Config, error) {
    jsonFile, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer jsonFile.Close()

	byteValue, err := io.ReadAll(jsonFile)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(byteValue, &config)
	if err != nil {
		return nil, err
	}

	if config.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}

	return &config, nil
}

func GetHomeDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return homeDir
}

func GetCwdDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil
}

func ReadFileToString(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

func GetIgnoreFiles(path string) ([]string, error) {
	ignoreFiles := []string{
		"patches",
		"node_modules",
		".DS_Store",
		".idea",
		".vscode",
		"dist",
		"build",
		"coverage",
		"out",
		"tmp",
		"temp",
		"*.log",
		"*.tmp",
	}

	path1 := filepath.Join(path, ".gitignore")
	path2 := filepath.Join(path, "..", ".gitignore")

	var content string

	if FileExists(path1) {
		c, err := ReadFileToString(path1)
		if err != nil {
			return nil, err
		}

		content = c
	} else if FileExists(path2) {
		c, err := ReadFileToString(path2)
		if err != nil {
			return nil, err
		}

		content = c
	} else {
		return ignoreFiles, nil
	}

	for line := range strings.SplitSeq(content, "\n") {
		if line != "" && line[0] != '#' {
			ignoreFiles = append(ignoreFiles, line)
		}
	}
	
	// Remove duplicates
	ignoreFilesMap := make(map[string]bool)
	for _, line := range ignoreFiles {
		ignoreFilesMap[line] = true
	}

	// Convert map keys back to slice
	uniqueIgnoreFiles := make([]string, 0, len(ignoreFilesMap))
	for line := range ignoreFilesMap {
		uniqueIgnoreFiles = append(uniqueIgnoreFiles, line)
	}

	return uniqueIgnoreFiles, nil
}

func WalkDir(path string, ignores []string) ([]string, error) {
	var files []string

	err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			for _, ignore := range ignores {
				if strings.Contains(filePath, ignore) {
					return filepath.SkipDir
				}
			}
			return nil
		}

		for _, ignore := range ignores {
			if strings.Contains(filePath, ignore) {
				return nil
			}
		}

		files = append(files, filePath)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return files, nil
}

func GetAllStories(path string) ([]Story, error) {
    ignores, err := GetIgnoreFiles(path)
	if err != nil {
		return nil, err
	}

	var stories []Story

	paths, err := WalkDir(path, ignores)
	if err != nil {
		return nil, err
	}

	for _, p := range paths {
		content, err := ReadFileToString(p)
		if err != nil {
			return nil, err
		}

		r, err := regexp.Compile(`export const (\w+): Story`)
		if err != nil {
			return nil, err
		}

		matches := r.FindAllStringSubmatch(content, -1)

		var componentName string
		parts := strings.Split(p, "/")
		if len(parts) <= 0 {
			return nil, fmt.Errorf("invalid path: %s", p)
		}
		lastPart := parts[len(parts)-1]
		fileParts := strings.Split(lastPart, ".")
		if len(fileParts) <= 0 {
			return nil, fmt.Errorf("invalid file name: %s", lastPart)
		}
		componentName = fileParts[0]

		if len(matches) > 0 {
			for _, match := range matches {
				var organismType string
				if strings.Contains(p, "99-drafts") {
					organismType = ORGANISM_TYPE_DRAFT
				} else if strings.Contains(p, "00-foundations") {
					organismType = ORGANISM_TYPE_FOUNDATION
				} else if strings.Contains(p, "10-atoms") {
					organismType = ORGANISM_TYPE_ATOM
				} else if strings.Contains(p, "20-molecules") {
					organismType = ORGANISM_TYPE_MOLECULE
				} else if strings.Contains(p, "30-organisms") {
					organismType = ORGANISM_TYPE_ORGANISM
				} else if strings.Contains(p, "40-templates") {
					organismType = ORGANISM_TYPE_TEMPLATE
				} else if strings.Contains(p, "50-pages") {
					organismType = ORGANISM_TYPE_PAGE
				} else {
					organismType = ORGANISM_TYPE_DRAFT
				}

				story := Story{
					Name: match[1],
					Path: p,
					ComponentName: componentName,
					OrganismType: organismType,
				}

				fmt.Printf("Found story: %s %s %s %s\n", story.ComponentName, story.Name, p, organismType)

				stories = append(stories, story)
			}
		}
	}

	return stories, nil
}

func (c *Config) Check(ctx context.Context, path string) ([]RegressionTestResult, error) {
	stories, err := GetAllStories(path)
	if err != nil {
		return nil, err
	}

	packageJsonPath := filepath.Join(path, "package.json")
	if !FileExists(packageJsonPath) {
		return nil, fmt.Errorf("package.json not found in %s", path)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, c.MaxThreads)
	results := make(chan *RegressionTestResult, len(stories))
	errors := make(chan error, len(stories))


	for _, story := range stories {
		wg.Add(1)

		go func(story *Story) {
			defer wg.Done()
			sem <- struct{}{}

			allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), chromedp.DefaultExecAllocatorOptions[:]...)
			defer cancel()
			ctx, cancelCtx := chromedp.NewContext(allocCtx)
			defer cancelCtx()

			ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
			defer cancelTimeout()

			result, err := story.Check(ctx, c)
			if err != nil {
				errors <- err
			} else {
				results <- result
			}

			<-sem
		}(&story)
	}

	wg.Wait()
	close(results)
	close(errors)

	var regressionTestResults []RegressionTestResult
	for result := range results {
		regressionTestResults = append(regressionTestResults, *result)
	}

	var regressionTestErrors []error
	for err := range errors {
		regressionTestErrors = append(regressionTestErrors, err)
	}

	if len(regressionTestErrors) > 0 {
		return nil, fmt.Errorf("errors occurred during regression test: %v", regressionTestErrors)
	}

	return regressionTestResults, nil
}

func GetEnvironment() *Environment {
	cwd := GetCwdDir()
	homeDir := GetHomeDir()

	homeConfigPath := filepath.Join(homeDir, ".config", "visage.json")
	localConfigPath := filepath.Join(cwd, "visage.json")

	var configPath string
	if FileExists(localConfigPath) {
		configPath = localConfigPath
	} else if FileExists(homeConfigPath) {
		configPath = homeConfigPath
	} else {
		log.Fatal("No config file found")
	}
	
	return &Environment{
		Cwd: cwd,
		Home: homeDir,
		ConfigPath: configPath,
	}
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: visage <command>")
	}

	cmd := os.Args[1]
	env := GetEnvironment()

	config, err := LoadConfig(env.ConfigPath)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := chromedp.NewContext(
		context.Background(), 
		chromedp.WithBrowserOption(
		),
	)

	defer cancel()

	switch cmd{
	case "check": 
		mockedFilePath := filepath.Join(env.Home, "code", "tuv-galaxy", "component-library", "project")
		res, err := config.Check(ctx, mockedFilePath)
		if err != nil {
			log.Fatal(err)
		}
		for _, result := range res {
			fmt.Printf("Component: %s, Status: %s\n", result.CurrentTest.Component, result.Status)
			fmt.Printf("Visual Hash: %s\n", result.CurrentTest.VisualHash)
			fmt.Printf("Expected Visual Hash: %s\n", result.ExpectedTest.VisualHash)
		}
	default:
		log.Fatal("Unknown command")
	}
}
