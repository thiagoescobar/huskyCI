package analysis

import (
	"fmt"
	"os"
	"strings"
	"time"

	docker "github.com/globocom/husky/dockers"
	"github.com/globocom/husky/types"
	"gopkg.in/mgo.v2/bson"
)

// DockerRun starts a new container, runs a given securityTest in it and then updates AnalysisCollection.
func DockerRun(RID string, analysis *types.Analysis, securityTest types.SecurityTest) {

	newContainer := types.Container{SecurityTest: securityTest}
	d := docker.Docker{}

	securityTest.Cmd = handlePrivateSSHKey(securityTest.Cmd)

	// step 1: create a new container.
	err := dockerRunCreateContainer(&d, analysis, securityTest, newContainer)
	if err != nil {
		fmt.Println("Error dockerRunCreateContainer():", err)
		return
	}

	// step 2: start created container.
	err = dockerRunStartContainer(&d, analysis)
	if err != nil {
		fmt.Println("Error dockerRunStartContainer():", err)
		return
	}

	// step 3: wait container finish running.
	err = dockerRunWaitContainer(&d, securityTest)
	if err != nil {
		// error timeout will enter here!
		if err := dockerRunRegisterError(&d, analysis); err != nil {
			fmt.Println("Error dockerRunRegisterError():", err)
			return
		}
		return
	}

	// step 4: read cmd output from container.
	cOutput, err := dockerRunReadOutput(&d, analysis)
	if err != nil {
		fmt.Println("Error dockerRunReadOutput():", err)
		return
	}

	// step 5: send output to the proper analysis result function.
	switch securityTest.Name {
	case "enry":
		EnryStartAnalysis(d.CID, cOutput, analysis.RID)
	case "gas":
		GasStartAnalysis(d.CID, cOutput)
	default:
		fmt.Println("Error: Could not find securityTest.Name.")
	}
}

// dockerRunCreateContainer creates a new container, updates the corresponding analysis into MongoDB and returns an error and a CID (container ID).
func dockerRunCreateContainer(d *docker.Docker, analysis *types.Analysis, securityTest types.SecurityTest, newContainer types.Container) error {

	analysisQuery := map[string]interface{}{"RID": analysis.RID}

	// step 1: creating a new container.
	CID, err := d.CreateContainer(*analysis, securityTest.Image, securityTest.Cmd)
	if err != nil {
		// error! update analysis with an error message and quit.
		newContainer.CStatus = "error"
		analysis.Containers = append(analysis.Containers, newContainer)
		err := UpdateOneDBAnalysis(analysisQuery, *analysis)
		if err != nil {
			fmt.Println("Error 1 dockerRunCreateContainer() UpdateOneDBAnalysis():", err)
			return err // implement a maxRetry?
		}
		return err // implement a maxRetry?
	}

	// step 2: update analysis with the container's information.
	d.CID = CID
	newContainer.CID = CID
	newContainer.CStatus = "created"
	analysis.Containers = append(analysis.Containers, newContainer)
	err = UpdateOneDBAnalysis(analysisQuery, *analysis)
	if err != nil {
		fmt.Println("Error 2 dockerRunCreateContainer() UpdateOneDBAnalysis():", err)
	}
	return err
}

// dockerRunStartContainer starts a container, updates the corresponding analysis into MongoDB and returns an error.
func dockerRunStartContainer(d *docker.Docker, analysis *types.Analysis) error {
	analysisQuery := map[string]interface{}{"containers.CID": d.CID}
	err := d.StartContainer()
	if err != nil {
		// error starting container. maxRetry?
		updateContainerAnalysisQuery := bson.M{
			"$set": bson.M{
				"containers.$.cStatus": "error",
			},
		}
		err = UpdateOneDBAnalysisContainer(analysisQuery, updateContainerAnalysisQuery)
		if err != nil {
			fmt.Println("Error updating AnalysisCollection (step 2-err):", err)
			return err
		}
		return err
	}
	startedAt := time.Now()
	updateContainerAnalysisQuery := bson.M{
		"$set": bson.M{
			"containers.$.cStatus":   "running",
			"containers.$.startedAt": startedAt,
		},
	}
	err = UpdateOneDBAnalysisContainer(analysisQuery, updateContainerAnalysisQuery)
	if err != nil {
		return err
	}
	return err
}

// dockerRunWaitContainer waits a container run its commands.
func dockerRunWaitContainer(d *docker.Docker, securityTest types.SecurityTest) error {
	err := d.WaitContainer(securityTest)
	return err
}

// dockerRunReadOutput reads the output of a container and updates the corresponding analysis into MongoDB.
func dockerRunReadOutput(d *docker.Docker, analysis *types.Analysis) (string, error) {
	analysisQuery := map[string]interface{}{"containers.CID": d.CID}
	cOutput, err := d.ReadOutput()
	if err != nil {
		fmt.Println("Error reading output from container", d.CID, ":", err)
		return "", err // implement a maxRetry?
	}
	finishedAt := time.Now()
	updateContainerAnalysisQuery := bson.M{
		"$set": bson.M{
			"containers.$.cStatus":    "finished",
			"containers.$.finishedAt": finishedAt,
			"containers.$.cOutput":    cOutput,
		},
	}
	err = UpdateOneDBAnalysisContainer(analysisQuery, updateContainerAnalysisQuery)
	if err != nil {
		fmt.Println("Error updating AnalysisCollection (step 4).", err)
		return "", err
	}
	return cOutput, err
}

// dockerRunRegisterError updates the corresponding analysis into MongoDB with an error status.
func dockerRunRegisterError(d *docker.Docker, analysis *types.Analysis) error {

	analysisQuery := map[string]interface{}{"containers.CID": d.CID}
	finishedAt := time.Now()
	updateContainerAnalysisQuery := bson.M{
		"$set": bson.M{
			"containers.$.cStatus":    "finished",
			"containers.$.finishedAt": finishedAt,
			"containers.$.cResult":    "failed",
			"containers.$.cOutput":    "Error waiting the container to finish.",
		},
	}
	err := UpdateOneDBAnalysisContainer(analysisQuery, updateContainerAnalysisQuery)
	if err != nil {
		fmt.Println("Error updating Analysis (dockerRunRegisterError): ", err)
		return err
	}
	return nil
}

func handlePrivateSSHKey(rawString string) string {
	cmdReplaced := strings.Replace(rawString, "GIT_PRIVATE_SSH_KEY", os.Getenv("GIT_PRIVATE_SSH_KEY"), -1)
	return cmdReplaced
}
