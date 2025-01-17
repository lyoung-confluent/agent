package config

import "os"

const (
	ConfigFile                 = "config-file"
	Endpoint                   = "endpoint"
	Token                      = "token"
	NoHTTPS                    = "no-https"
	ShutdownHookPath           = "shutdown-hook-path"
	DisconnectAfterJob         = "disconnect-after-job"
	DisconnectAfterIdleTimeout = "disconnect-after-idle-timeout"
	EnvVars                    = "env-vars"
	Files                      = "files"
	FailOnMissingFiles         = "fail-on-missing-files"
)

var ValidConfigKeys = []string{
	ConfigFile,
	Endpoint,
	Token,
	NoHTTPS,
	ShutdownHookPath,
	DisconnectAfterJob,
	DisconnectAfterIdleTimeout,
	EnvVars,
	Files,
	FailOnMissingFiles,
}

type HostEnvVar struct {
	Name  string
	Value string
}

type FileInjection struct {
	HostPath    string
	Destination string
}

func (f *FileInjection) CheckFileExists() error {
	_, err := os.Stat(f.HostPath)
	if err != nil {
		return err
	}

	return nil
}
