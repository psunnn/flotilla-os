package services

import (
	"github.com/stitchfix/flotilla-os/clients/logs"
	"github.com/stitchfix/flotilla-os/config"
	"github.com/stitchfix/flotilla-os/state"
	"net/http"
)

type LogService interface {
	Logs(runID string, lastSeen *string) (string, *string, error)
	LogsText(runID string, w http.ResponseWriter) error
}

type logService struct {
	sm state.Manager
	lc logs.Client
}

func NewLogService(conf config.Config, sm state.Manager, lc logs.Client) (LogService, error) {
	return &logService{sm: sm, lc: lc}, nil
}

func (ls *logService) Logs(runID string, lastSeen *string) (string, *string, error) {
	run, err := ls.sm.GetRun(runID)
	if err != nil {
		return "", nil, err
	}

	if run.Status != state.StatusRunning && run.Status != state.StatusStopped {
		// Won't have logs yet
		return "", nil, nil
	}

	if run.ExecutableType == nil {
		defaultExecutableType := state.ExecutableTypeDefinition
		run.ExecutableType = &defaultExecutableType
	}

	if run.ExecutableID == nil {
		run.ExecutableID = &run.DefinitionID
	}
	executable, err := ls.sm.GetExecutableByTypeAndID(*run.ExecutableType, *run.ExecutableID)

	if err != nil && *run.Engine == state.ECSEngine {
		return "", nil, err
	}

	return ls.lc.Logs(executable, run, lastSeen)
}

func (ls *logService) LogsText(runID string, w http.ResponseWriter) error {
	run, err := ls.sm.GetRun(runID)
	if err != nil {
		return err
	}

	if run.Status != state.StatusRunning && run.Status != state.StatusStopped {
		// Won't have logs yet
		return nil
	}

	if run.ExecutableType == nil {
		defaultExecutableType := state.ExecutableTypeDefinition
		run.ExecutableType = &defaultExecutableType
	}
	if run.ExecutableID == nil {
		run.ExecutableID = &run.DefinitionID
	}
	executable, err := ls.sm.GetExecutableByTypeAndID(*run.ExecutableType, *run.ExecutableID)

	return ls.lc.LogsText(executable, run, w)
}
