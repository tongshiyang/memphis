// Copyright 2022-2023 The Memphis.dev Authors
// Licensed under the Memphis Business Source License 1.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// Changed License: [Apache License, Version 2.0 (https://www.apache.org/licenses/LICENSE-2.0), as published by the Apache Foundation.
//
// https://github.com/memphisdev/memphis/blob/master/LICENSE
//
// Additional Use Grant: You may make use of the Licensed Work (i) only as part of your own product or service, provided it is not a message broker or a message queue product or service; and (ii) provided that you do not use, provide, distribute, or make available the Licensed Work as a Service.
// A "Service" is a commercial offering, product, hosted, or managed service, that allows third parties (other than your own employees and contractors acting on your behalf) to access and/or use the Licensed Work or a substantial set of the features or functionality of the Licensed Work to third parties as a software-as-a-service, platform-as-a-service, infrastructure-as-a-service or other similar services that compete with Licensor products or services.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"strings"

	"github.com/google/go-github/github"
	"github.com/memphisdev/memphis/models"
	"github.com/memphisdev/memphis/utils"

	"github.com/gin-gonic/gin"
)

type FunctionsHandler struct{}

func (fh FunctionsHandler) GetAllFunctions(c *gin.Context) {
	user, err := getUserDetailsFromMiddleware(c)
	if err != nil {
		serv.Errorf("GetAllFunctions at getUserDetailsFromMiddleware: %v", err.Error())
		c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
		return
	}

	functionsResult, err := fh.GetFunctions(user.TenantName)
	if err != nil {
		if strings.Contains(err.Error(), "403 API rate limit exceeded") {
			serv.Warnf("[tenant: %v][user: %v]GetAllFunctions at GetFunctions: %v", user.TenantName, user.Username, err.Error())
			c.AbortWithStatusJSON(SHOWABLE_ERROR_STATUS_CODE, gin.H{"message": "Github's rate limit has been reached, please try again in 1 hour"})
			return
		} else {
			serv.Errorf("[tenant: %v][user: %v]GetAllFunctions at GetFunctions: %v", user.TenantName, user.Username, err.Error())
			c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
			return
		}
	}

	c.IndentedJSON(200, gin.H{"scm_integrated": functionsResult.ScmIntegrated, "other": functionsResult.OtherFunctions, "installed": functionsResult.InstalledFunctions, "connected_repos": functionsResult.ConnectedRepos})
}

func (fh FunctionsHandler) GetFunctions(tenantName string) (models.FunctionsRes, error) {
	contentDetailsOfSelectedRepos, scmIntegrated, err := GetContentOfSelectedRepos(tenantName)
	if err != nil {
		return models.FunctionsRes{}, err
	}
	functions, err := GetFunctionsDetails(contentDetailsOfSelectedRepos)
	if err != nil {
		return models.FunctionsRes{}, err
	}

	installedFunctions := functions["installed"]
	OtherFunctions := functions["other"]
	if len(installedFunctions) == 0 {
		installedFunctions = []models.FunctionResult{}
	}

	if len(OtherFunctions) == 0 {
		OtherFunctions = []models.FunctionResult{}
	}

	var lastModified *time.Time
	OtherFunctions = []models.FunctionResult{}
	for _, function := range functions["other"] {
		if function.Owner == memphisDevFunctionsOwnerName && function.Repo == memphisDevFunctionsRepoName {
			otherFunctionResult := models.FunctionResult{
				FunctionName:     function.FunctionName,
				Description:      function.Description,
				Tags:             function.Tags,
				Runtime:          function.Runtime,
				Dependencies:     function.Dependencies,
				EnvironmentVars:  function.EnvironmentVars,
				Memory:           function.Memory,
				Storage:          function.Storage,
				Handler:          function.Handler,
				Scm:              "github",
				Repo:             function.Repo,
				Branch:           function.Branch,
				Owner:            function.Owner,
				Language:         function.Language,
				Version:          -1,
				IsValid:          function.IsValid,
				InvalidReason:    function.InvalidReason,
				InProgress:       false,
				UpdatesAvailable: false,
				ByMemphis:        function.ByMemphis,
				TenantName:       function.TenantName,
			}
			OtherFunctions = append(OtherFunctions, otherFunctionResult)
			lastModified = function.LastCommit
		}
	}

	memphisDevFucntions := []map[string]interface{}{}
	memphisFunc := map[string]interface{}{
		"repo_name":     memphisFunctions["repo_name"].(string),
		"branch":        memphisFunctions["branch"].(string),
		"owner":         memphisFunctions["repo_owner"].(string),
		"last_modified": lastModified,
	}
	memphisDevFucntions = append(memphisDevFucntions, memphisFunc)

	allFunctions := models.FunctionsRes{
		InstalledFunctions: installedFunctions,
		OtherFunctions:     OtherFunctions,
		ScmIntegrated:      scmIntegrated,
		ConnectedRepos:     memphisDevFucntions,
	}

	return allFunctions, nil
}

func validateYamlContent(yamlMap map[string]interface{}) error {
	requiredFields := []string{"function_name", "runtime", "dependencies"}
	missingFields := make([]string, 0)
	for _, field := range requiredFields {
		if _, exists := yamlMap[field]; !exists {
			missingFields = append(missingFields, field)
		}
	}

	if len(missingFields) > 0 {
		return fmt.Errorf("Missing fields: %v\n", missingFields)
	}
	return nil
}

func (fh FunctionsHandler) GetFunctionDetails(c *gin.Context) {
	var body models.GetFunctionDetails
	ok := utils.Validate(c, &body, false, nil)
	if !ok {
		return
	}
	user, err := getUserDetailsFromMiddleware(c)
	if err != nil {
		serv.Errorf("GetFunctionDetails at getUserDetailsFromMiddleware: %v", err.Error())
		c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
		return
	}

	var accessToken string
	var githubClient *github.Client
	var response interface{}
	isIntegrationConnected := false
	if tenantInetgrations, ok := IntegrationsConcurrentCache.Load(user.TenantName); ok {
		if _, ok := tenantInetgrations[body.Scm].(models.Integration); ok {
			_, accessToken, githubClient, err = getGithubClient(user.TenantName)
			if err != nil {
				serv.Errorf("GetFunctionDetails at getGithubClient: %v", err.Error())
				c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
				return
			}
			isIntegrationConnected = true
		} else {
			body.Repository = memphisDevFunctionsRepoName
			body.Owner = memphisDevFunctionsOwnerName
			body.Branch = memphisDevFunctionsBranchName
		}
	} else {
		body.Repository = memphisDevFunctionsRepoName
		body.Owner = memphisDevFunctionsOwnerName
		body.Branch = memphisDevFunctionsBranchName
	}
	if !isIntegrationConnected {
		githubClient = getGithubClientWithoutAccessToken()
	}
	if (body.Type != "file" && body.Type != "dir") || body.Path == "" {
		getRepoContentURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/?ref=%s", body.Owner, body.Repository, body.Branch)
		response, err = getRepoContent(getRepoContentURL, accessToken, body)
		if err != nil {
			serv.Errorf("GetFunctionDetails at getRepoContent: %v", err.Error())
			c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
			return
		}
		if !isIntegrationConnected {
			c.IndentedJSON(200, gin.H{"content": response})
			return
		}
	} else if body.Type == "file" {
		filePath := body.Path
		fileContent, _, _, err := githubClient.Repositories.GetContents(context.Background(), body.Owner, body.Repository, filePath, &github.RepositoryContentGetOptions{
			Ref: body.Branch})
		if err != nil {
			if strings.Contains(err.Error(), "404 Not Found") || strings.Contains(err.Error(), "No commit found for the ref test") {
				serv.Warnf("GetFunctionDetails at githubClient.Repositories.GetContents: %v", err.Error())
				message := fmt.Sprintf("The file %s in repository %s in branch %s in organization %s not found", body.Path, body.Repository, body.Branch, body.Owner)
				c.AbortWithStatusJSON(SHOWABLE_ERROR_STATUS_CODE, gin.H{"message": message})
				return
			}
			serv.Errorf("GetFunctionDetails at githubClient.Repositories.GetContents: %v", err.Error())
			c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
			return
		}
		if fileContent != nil {
			content, err := fileContent.GetContent()
			if err != nil {
				serv.Errorf("GetFunctionDetails at fileContent.GetContent: %v", err.Error())
				c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
				return
			}

			response = content
		}
	} else if body.Type == "dir" {
		getRepoContentURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s", body.Owner, body.Repository, body.Path, body.Branch)
		response, err = getRepoContent(getRepoContentURL, accessToken, body)
		if err != nil {
			serv.Errorf("GetFunctionDetails at getRepoContent: %v", err.Error())
			c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
			return
		}
	}
	c.IndentedJSON(200, gin.H{"content": response})
}

func getRepoContent(url, accessToken string, body models.GetFunctionDetails) (interface{}, error) {
	var response interface{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return response, err
	}

	if body.Repository != memphisDevFunctionsRepoName {
		req.Header.Set("Authorization", "token "+accessToken)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return response, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return response, err
	}

	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		return response, err
	}

	return response, nil
}

func GetFunctionsDetails(functionsDetails map[string][]functionDetails) (map[string][]models.FunctionResult, error) {
	functions := map[string][]models.FunctionResult{}
	for key, functionDetails := range functionsDetails {
		for _, funcDetailsPerInstalled := range functionDetails {
			fucntionContentMap := funcDetailsPerInstalled.ContentMap
			commit := funcDetailsPerInstalled.Commit
			link := funcDetailsPerInstalled.DirectoryUrl
			repo := funcDetailsPerInstalled.RepoName
			branch := funcDetailsPerInstalled.Branch
			owner := funcDetailsPerInstalled.Owner
			tenantName := funcDetailsPerInstalled.TenantName
			isValid := funcDetailsPerInstalled.IsValid
			invalidReason := funcDetailsPerInstalled.InvalidReason
			tagsInterfaceSlice, ok := fucntionContentMap["tags"].([]interface{})
			tagsStrings := []string{}
			if ok {
				tagsStrings = make([]string, len(fucntionContentMap["tags"].([]interface{})))
				for i, tag := range tagsInterfaceSlice {
					tagMap := tag.(map[interface{}]interface{})
					for _, v := range tagMap {
						if str, ok := v.(string); ok {
							tagsStrings[i] = str
						}
					}
				}
			}

			environmentVarsStrings := []map[string]interface{}{}
			environmentVarsInterfaceSlice, ok := fucntionContentMap["environment_vars"].([]interface{})
			if ok {
				for _, environmentVarInterface := range environmentVarsInterfaceSlice {
					environmentVarMap, _ := environmentVarInterface.(map[interface{}]interface{})
					environmentVar := make(map[string]interface{})
					for k, v := range environmentVarMap {
						if key, ok := k.(string); ok {
							if val, ok := v.(string); ok {
								environmentVar[key] = val
							}
						}
					}
					environmentVarsStrings = append(environmentVarsStrings, environmentVar)
				}
			}
			description, ok := fucntionContentMap["description"].(string)
			if !ok {
				description = ""
			}

			runtime, ok := fucntionContentMap["runtime"].(string)
			var language string
			if ok {
				regex := regexp.MustCompile(`[0-9]+|\\.$`)
				language = regex.ReplaceAllString(runtime, "")
				language = strings.TrimRight(language, ".")
				if strings.Contains(language, "-edge") {
					language = strings.Trim(language, ".-edge")
				}
			}

			byMemphis := false
			if repo == memphisDevFunctionsRepoName && owner == memphisDevFunctionsOwnerName {
				byMemphis = true
			}

			handler := ""
			if _, ok := fucntionContentMap["handler"].(string); ok {
				handler = fucntionContentMap["handler"].(string)
			}
			var lastCommit *time.Time
			if commit != nil {
				lastCommit = &*commit.Commit.Committer.Date
			}

			functionDetails := models.FunctionResult{
				FunctionName:     fucntionContentMap["function_name"].(string),
				Description:      description,
				Tags:             tagsStrings,
				Runtime:          runtime,
				Dependencies:     fucntionContentMap["dependencies"].(string),
				EnvironmentVars:  environmentVarsStrings,
				Memory:           fucntionContentMap["memory"].(int),
				Storage:          fucntionContentMap["storage"].(int),
				Handler:          handler,
				Scm:              "github",
				Repo:             repo,
				Branch:           branch,
				Owner:            owner,
				LastCommit:       lastCommit,
				Link:             link,
				Language:         language,
				InProgress:       false,
				UpdatesAvailable: false,
				ByMemphis:        byMemphis,
				TenantName:       tenantName,
				IsValid:          isValid,
				InvalidReason:    invalidReason,
			}

			functions[key] = append(functions[key], functionDetails)
		}
	}
	return functions, nil
}