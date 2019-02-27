package dockercompose

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	pty "github.com/kr/pty"
	api "github.com/semaphoreci/agent/pkg/api"
	executors "github.com/semaphoreci/agent/pkg/executors"
)

type DockerComposeExecutor struct {
	executors.Executor

	tmpDirectory              string
	dockerConfiguration       api.Compose
	dockerComposeManifestPath string
	eventHandler              *executors.EventHandler
	terminal                  *exec.Cmd
	tty                       *os.File
	stdin                     io.Writer
	stdoutScanner             *bufio.Scanner
}

func NewDockerComposeExecutor(dockerConfiguration api.Compose) *DockerComposeExecutor {
	return &DockerComposeExecutor{
		dockerConfiguration:       dockerConfiguration,
		dockerComposeManifestPath: "/tmp/docker-compose.yml",
		tmpDirectory:              "/tmp/111", // make a better random name
	}
}

func (e *DockerComposeExecutor) Prepare() int {
	compose := ConstructComposeFile(e.dockerConfiguration)
	ioutil.WriteFile(e.dockerComposeManifestPath, []byte(compose), 0644)

	e.terminal = exec.Command(
		"docker-compose",
		"-f",
		e.dockerComposeManifestPath,
		"run",
		"-v",
		fmt.Sprintf("%s:%s:ro", e.tmpDirectory, e.tmpDirectory),
		"main",
		"bash",
	)

	return 0
}

func (e *DockerComposeExecutor) Start() int {
	log.Printf("[SHELL] Starting stateful shell")

	tty, err := pty.Start(e.terminal)
	if err != nil {
		log.Printf("[SHELL] Failed to start stateful shell err: %+v", err)
		return 1
	}

	e.stdin = tty
	e.tty = tty

	time.Sleep(1000)

	e.silencePromptAndDisablePS1()

	return 0
}

func (e *DockerComposeExecutor) silencePromptAndDisablePS1() {
	everythingIsReadyMark := "87d140552e404df69f6472729d2b2c3"

	e.stdin.Write([]byte("export PS1=''\n"))
	e.stdin.Write([]byte("stty -echo\n"))
	e.stdin.Write([]byte("cd ~\n"))
	e.stdin.Write([]byte("echo '" + everythingIsReadyMark + "'\n"))

	stdoutScanner := bufio.NewScanner(e.tty)

	//
	// At this point, the terminal is still echoing the output back to stdout
	// we ignore the entered command, and look for the magic mark in the output
	//
	// Example content of output before ready mark:
	//
	//   export PS1=''
	//   stty -echo
	//   echo + '87d140552e404df69f6472729d2b2c3'
	//   vagrant@boxbox:~/code/agent/pkg/executors/shell$ export PS1=''
	//   stty -echo
	//   echo '87d140552e404df69f6472729d2b2c3'
	//

	// We wait until marker is displayed in the output

	log.Println("[SHELL] Waiting for initialization")

	for stdoutScanner.Scan() {
		text := stdoutScanner.Text()

		log.Printf("[SHELL] (tty) %s\n", text)
		if !strings.Contains(text, "echo") && strings.Contains(text, everythingIsReadyMark) {
			break
		}
	}

	log.Println("[SHELL] Initialization complete")
}

func (e *DockerComposeExecutor) ExportEnvVars(envVars []api.EnvVar, callback executors.EventHandler) int {
	commandStartedAt := int(time.Now().Unix())
	directive := fmt.Sprintf("Exporting environment variables")
	exitCode := 0

	callback(executors.NewCommandStartedEvent(directive))

	defer func() {
		commandFinishedAt := int(time.Now().Unix())

		callback(executors.NewCommandFinishedEvent(
			directive,
			exitCode,
			commandStartedAt,
			commandFinishedAt,
		))
	}()

	envFile := ""

	for _, e := range envVars {
		callback(executors.NewCommandOutputEvent(fmt.Sprintf("Exporting %s\n", e.Name)))

		value, err := base64.StdEncoding.DecodeString(e.Value)

		if err != nil {
			exitCode = 1
			return exitCode
		}

		envFile += fmt.Sprintf("export %s=%s\n", e.Name, ShellQuote(string(value)))
	}

	envPath := fmt.Sprintf("%s/.env", e.tmpDirectory)
	err := ioutil.WriteFile(envPath, []byte(envFile), 0644)

	if err != nil {
		exitCode = 255
		return exitCode
	}

	cmd := fmt.Sprintf("source %s", envPath)
	exitCode = e.RunCommand(cmd, executors.DevNullEventHandler)
	if exitCode != 0 {
		return exitCode
	}

	cmd = fmt.Sprintf("echo 'source %s' >> ~/.bash_profile", envPath)
	exitCode = e.RunCommand(cmd, executors.DevNullEventHandler)
	if exitCode != 0 {
		return exitCode
	}

	return exitCode
}

func (e *DockerComposeExecutor) InjectFiles(files []api.File, callback executors.EventHandler) int {
	directive := fmt.Sprintf("Injecting Files")
	commandStartedAt := int(time.Now().Unix())
	exitCode := 0

	callback(executors.NewCommandStartedEvent(directive))

	for _, f := range files {
		output := fmt.Sprintf("Injecting %s with file mode %s\n", f.Path, f.Mode)

		callback(executors.NewCommandOutputEvent(output))

		content, err := base64.StdEncoding.DecodeString(f.Content)

		if err != nil {
			callback(executors.NewCommandOutputEvent("Failed to decode content of file.\n"))
			exitCode = 1
			return exitCode
		}

		tmpPath := fmt.Sprintf("%s/file", e.tmpDirectory)

		err = ioutil.WriteFile(tmpPath, []byte(content), 0644)
		if err != nil {
			callback(executors.NewCommandOutputEvent(err.Error() + "\n"))
			exitCode = 255
			break
		}

		destPath := ""

		if f.Path[0] == '/' {
			destPath = f.Path
		} else {
			destPath = UserHomeDir() + "/" + f.Path
		}

		cmd := fmt.Sprintf("mkdir -p %s", path.Dir(destPath))
		exitCode = e.RunCommand(cmd, executors.DevNullEventHandler)
		if exitCode != 0 {
			output := fmt.Sprintf("Failed to create destination path %s", destPath)
			callback(executors.NewCommandOutputEvent(output + "\n"))
			break
		}

		cmd = fmt.Sprintf("cp %s %s", tmpPath, destPath)
		exitCode = e.RunCommand(cmd, executors.DevNullEventHandler)
		if exitCode != 0 {
			output := fmt.Sprintf("Failed to move to destination path %s %s", tmpPath, destPath)
			callback(executors.NewCommandOutputEvent(output + "\n"))
			break
		}

		cmd = fmt.Sprintf("chmod %s %s", f.Mode, destPath)
		exitCode = e.RunCommand(cmd, executors.DevNullEventHandler)
		if exitCode != 0 {
			output := fmt.Sprintf("Failed to set file mode to %s", f.Mode)
			callback(executors.NewCommandOutputEvent(output + "\n"))
			break
		}
	}

	commandFinishedAt := int(time.Now().Unix())

	callback(executors.NewCommandFinishedEvent(
		directive,
		exitCode,
		commandStartedAt,
		commandFinishedAt,
	))

	return exitCode
}

func (e *DockerComposeExecutor) RunCommand(command string, callback executors.EventHandler) int {
	var err error

	log.Printf("[SHELL] Running command: %s", command)

	cmdFilePath := fmt.Sprintf("%s/current-agent-cmd", e.tmpDirectory)
	startMark := "87d140552e404df69f6472729d2b2c1"
	finishMark := "97d140552e404df69f6472729d2b2c2"

	commandStartedAt := int(time.Now().Unix())
	exitCode := 1

	defer func() {
		commandFinishedAt := int(time.Now().Unix())

		callback(executors.NewCommandFinishedEvent(
			command,
			exitCode,
			commandStartedAt,
			commandFinishedAt,
		))
	}()

	commandEndRegex := regexp.MustCompile(finishMark + " " + `(\d)`)
	streamEvents := false

	//
	// Multiline commands don't work very well with the start/finish marker scheme.
	// To circumvent this, we are storing the command in a file
	//
	err = ioutil.WriteFile(cmdFilePath, []byte(command), 0644)

	if err != nil {
		callback(executors.NewCommandStartedEvent(command))
		callback(executors.NewCommandOutputEvent(fmt.Sprintf("Failed to run command: %+v\n", err)))

		return 1
	}

	// Constructing command with start and end markers:
	//
	// 1. display START marker
	// 2. execute the command file by sourcing it
	// 3. save the original exit status
	// 4. display the END marker with the exit status
	// 5. return the original exit status to the caller
	//

	commandWithStartAndEndMarkers := strings.Join([]string{
		fmt.Sprintf("echo '%s'", startMark),
		fmt.Sprintf("source %s", cmdFilePath),
		"AGENT_CMD_RESULT=$?",
		fmt.Sprintf(`echo "%s $AGENT_CMD_RESULT"`, finishMark),
		"echo \"exit $AGENT_CMD_RESULT\"|sh\n",
	}, ";")

	e.stdin.Write([]byte(commandWithStartAndEndMarkers))

	stdoutScanner := bufio.NewScanner(e.tty)

	log.Println("[SHELL] Scan started")
	for stdoutScanner.Scan() {
		t := stdoutScanner.Text()
		log.Printf("[SHELL] (tty) %s\n", t)

		if strings.Contains(t, startMark) {
			streamEvents = true

			callback(executors.NewCommandStartedEvent(command))

			continue
		}

		if strings.Contains(t, finishMark) {
			streamEvents = false

			if match := commandEndRegex.FindStringSubmatch(t); len(match) == 2 {
				exitCode, err = strconv.Atoi(match[1])

				if err != nil {
					log.Printf("[SHELL] Panic while parsing exit status, err: %+v", err)

					callback(executors.NewCommandOutputEvent("Failed to read command exit code\n"))
				}

			} else {
				exitCode = 1
				callback(executors.NewCommandOutputEvent("Failed to read command exit code\n"))
			}

			break
		}

		if streamEvents {
			callback(executors.NewCommandOutputEvent(t + "\n"))
		}
	}

	return exitCode
}

func (e *DockerComposeExecutor) Stop() int {
	err := e.terminal.Process.Kill()

	if err != nil {
		return 0
	}

	return 0
}

func (e *DockerComposeExecutor) Cleanup() int {
	return 0
}

func UserHomeDir() string {
	if runtime.GOOS == "windows" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		return home
	}
	return os.Getenv("HOME")
}

func ShellQuote(s string) string {
	pattern := regexp.MustCompile(`[^\w@%+=:,./-]`)

	if len(s) == 0 {
		return "''"
	}
	if pattern.MatchString(s) {
		return "'" + strings.Replace(s, "'", "'\"'\"'", -1) + "'"
	}

	return s
}
