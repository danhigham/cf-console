package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"os/exec"

	"github.com/cloudfoundry/cli/cf/configuration/config_helpers"
	"github.com/cloudfoundry/cli/cf/configuration/core_config"
	"github.com/cloudfoundry/cli/plugin"
	"github.com/mitchellh/colorstring"
)

type AppSearchResults struct {
	Resources []AppSearchResoures `json:"resources"`
}

type AppSearchResoures struct {
	Metadata AppSearchMetaData `json:"metadata"`
	Entity   AppSearchEntity   `json:"entity"`
}

type AppSearchMetaData struct {
	Guid string `json:"guid"`
	Url  string `json:"url"`
}

type AppSearchEntity struct {
	Instances         int    `json:"instances"`
	Command           string `json:"command"`
	DetectedCommand   string `json:"detected_start_command"`
	Buildpack         string `json:"buildpack"`
	DetectedBuildpack string `json:"detected_buildpack"`
}

type AppSummary struct {
	Guid							string `json:"guid"`
	Name							string `json:"name"`
	Diego							bool 	 `json:"diego"`
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stdout, "error:", err)
		os.Exit(1)
	}
}

func main() {
	plugin.Start(&ConsolePlugin{})
}

type ConsolePlugin struct {
	Instances int
}

func (plugin ConsolePlugin) Run(cliConnection plugin.CliConnection, args []string) {

	// Find app guid
	appName := args[1]
	guid, entity := plugin.FindAppGuid(cliConnection, appName)

	// Is it running diego
	summary := plugin.Summary(cliConnection, guid)

	instances := entity.Instances

	if summary.Diego == false {

		// Update the app to start tmate
		plugin.UpdateForTmate(cliConnection, guid, "sleep 3600")

		// Add Instance
		instances = entity.Instances + 1
		plugin.ChangeInstanceCount(cliConnection, guid, instances)

	} else {



	}

	lastDate := plugin.GetLatestLogDate(cliConnection, appName)

	// From this point on if anything goes wrong and the user interrupts the
	// process, we should catch it and clean up.

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func(){
		for _ = range c {
			// sig is a ^C, handle it

			// Reduce instance count
			plugin.ChangeInstanceCount(cliConnection, guid, instances - 1)

			// Reset start command
			plugin.ChangeAppCommand(cliConnection, guid, entity.Command)
		}
	}()

	plugin.WaitAndConnect(cliConnection, appName, instances, lastDate)
	// SSH has finished. Clean up

	// Reduce instance count
	plugin.ChangeInstanceCount(cliConnection, guid, instances - 1)

	// Reset start command
	plugin.ChangeAppCommand(cliConnection, guid, entity.Command)
}

func (plugin ConsolePlugin) WaitAndConnect(cliConnection plugin.CliConnection, appName string, instances int, lastDate string) {

	plugin.Log("Waiting for SSH endpoint.\n", false)

	// Regex for tmate log line
	exp := fmt.Sprintf("\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}.\\d{2}-\\d{4}\\s\\[App\\/%v\\]\\s{6}[^\\n]+\\s([^\\s]+\\@[a-z1-9]+\\.tmate\\.io)", instances-1)
	reDate := regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.\d{2}-\d{4})\s`)

	re := regexp.MustCompile(exp)

	// Wait until new ssh endpoint turns up
	cmd := []string{"logs", appName, "--recent"}

	var ssh_endpoint string

	for ssh_endpoint == "" {
		output, _ := cliConnection.CliCommandWithoutTerminalOutput(cmd...)

		for _, v := range output {
			matches := re.FindAllStringSubmatch(v, -1)
			lineDate := reDate.FindStringSubmatch(v)

			if lineDate != nil {
				for _, m := range matches {
					if lineDate[0] > lastDate {
						ssh_endpoint = m[1]
					}
				}
			}

		}
	}

	plugin.Log(fmt.Sprintf("SSHing to %v\n", ssh_endpoint), false)

	// Launch SSH
	ps := exec.Command("ssh", ssh_endpoint)
	ps.Stdout = os.Stdout
	ps.Stderr = os.Stderr
	ps.Stdin = os.Stdin

	ps.Run()
}

func (plugin ConsolePlugin) GetLatestLogDate(cliConnection plugin.CliConnection, appName string) string {

	plugin.Log("Checking app log datestamps.\n", false)

	// Regex for tmate log line
	re := regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.\d{2}-\d{4})\s`)

	// Find last log entry
	cmd := []string{"logs", appName, "--recent"}

	output, _ := cliConnection.CliCommandWithoutTerminalOutput(cmd...)
	lastLine := output[len(output)-1]

	matches := re.FindStringSubmatch(lastLine)
	return matches[0]
}

func (plugin ConsolePlugin) KillInstanceZero(cliConnection plugin.CliConnection, appGuid string) {

	plugin.Log("Killing instance 0.\n", false)

	// Kill the first instance and wait for it to come back up
	appURL := fmt.Sprintf("/v2/apps/%v/instances/0", appGuid)
	cmd := []string{"curl", appURL, "-X", "DELETE"}

	cliConnection.CliCommandWithoutTerminalOutput(cmd...)
}

func (plugin ConsolePlugin) ChangeAppCommand(cliConnection plugin.CliConnection, appGuid string, startCmd string) {
	plugin.Log(fmt.Sprintf("Updating app start command to '%v'.\n", startCmd), false)

	startCmd = strings.Replace(startCmd, "\"", "\\\"", -1)

	appURL := fmt.Sprintf("/v2/apps/%v", appGuid)
	newCommand := fmt.Sprintf("{\"command\":\"%v\"}", startCmd)

	cmd := []string{"curl", appURL, "-X", "PUT", "-d", newCommand}

	cliConnection.CliCommandWithoutTerminalOutput(cmd...)

}

func (plugin ConsolePlugin) UpdateForTmate(cliConnection plugin.CliConnection, appGuid string, command string) {
	plugin.Log("Updating app to connect to tmate.\n", false)

	tmateCmd := "curl -s https://raw.githubusercontent.com/danhigham/cf-console/master/install.sh > /tmp/install.sh && bash /tmp/install.sh"
	compCmd := tmateCmd

	if command != "" {
		compCmd = fmt.Sprintf("%v && %v", tmateCmd, command)
	}

	plugin.ChangeAppCommand(cliConnection, appGuid, compCmd)
}

func (plugin ConsolePlugin) ChangeInstanceCount(cliConnection plugin.CliConnection, appGuid string, instances int) {

	plugin.Log(fmt.Sprintf("Changing instance count to %v.\n", instances), false)

	appURL := fmt.Sprintf("/v2/apps/%v", appGuid)
	newCommand := fmt.Sprintf("{\"instances\":%v}", instances)
	cmd := []string{"curl", appURL, "-X", "PUT", "-d", newCommand}

	cliConnection.CliCommandWithoutTerminalOutput(cmd...)
}

func (plugin ConsolePlugin) FindAppGuid(cliConnection plugin.CliConnection, appName string) (string, AppSearchEntity) {

	plugin.Log(fmt.Sprintf("Finding app guid for %v ... ", appName), false)

	confRepo := core_config.NewRepositoryFromFilepath(config_helpers.DefaultFilePath(), fatalIf)
	spaceGuid := confRepo.SpaceFields().Guid

	appQuery := fmt.Sprintf("/v2/spaces/%v/apps?q=name:%v&inline-relations-depth=1", spaceGuid, appName)
	cmd := []string{"curl", appQuery}

	output, _ := cliConnection.CliCommandWithoutTerminalOutput(cmd...)
	res := &AppSearchResults{}
	json.Unmarshal([]byte(strings.Join(output, "")), &res)

	plugin.Log(fmt.Sprintf("%v \n", res.Resources[0].Metadata.Guid), true)

	return res.Resources[0].Metadata.Guid, res.Resources[0].Entity
}

func (plugin ConsolePlugin) Summary(cliConnection plugin.CliConnection, guid string) (AppSummary) {

	appQuery := fmt.Sprintf("/v2/apps/%v/summary", guid)
	cmd := []string{"curl", appQuery}

	output, _ := cliConnection.CliCommandWithoutTerminalOutput(cmd...)
	res := AppSummary{}
	json.Unmarshal([]byte(strings.Join(output, "")), &res)

	plugin.Log(fmt.Sprintf("%v \n", res), true)

	return res
}


func (ConsolePlugin) Log(text string, skipChevron bool) {
	if skipChevron {
		fmt.Printf("%v", colorstring.Color("[light_gray]" + text))
		return
	}
	fmt.Printf("%v %v", colorstring.Color("[blue]>"), colorstring.Color("[light_gray]" + text))
}

func (ConsolePlugin) GetMetadata() plugin.PluginMetadata {
	return plugin.PluginMetadata{
		Name: "Console",
		Commands: []plugin.Command{
			{
				Name:     "console",
				HelpText: "Start a live console",
			},
		},
	}
}
