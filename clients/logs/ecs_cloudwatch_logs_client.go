package logs

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/pkg/errors"
	"github.com/stitchfix/flotilla-os/config"
	"github.com/stitchfix/flotilla-os/exceptions"
	"github.com/stitchfix/flotilla-os/state"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

//
// ECSCloudWatchLogsClient corresponds with the aws logs driver
// for ECS and returns logs for runs
//
type ECSCloudWatchLogsClient struct {
	logRetentionInDays int64
	logNamespace       string
	logStreamPrefix    string
	logsClient         logsClient
	logger             *log.Logger
}

//
// Name returns the name of the logs client
//
func (cwl *ECSCloudWatchLogsClient) Name() string {
	return "ecs-cloudwatch"
}

//
// Initialize sets up the ECSCloudWatchLogsClient
//
func (cwl *ECSCloudWatchLogsClient) Initialize(conf config.Config) error {
	confLogOptions := conf.GetStringMapString("ecs.log.driver.options")

	awsRegion := confLogOptions["awslogs-region"]
	if len(awsRegion) == 0 {
		awsRegion = conf.GetString("aws_default_region")
	}

	if len(awsRegion) == 0 {
		return errors.Errorf(
			"ECSCloudWatchLogsClient needs one of [ecs.log.driver.options.awslogs-region] or [aws_default_region] set in config")
	}

	//
	// log.namespace in conf takes precedence over log.driver.options.awslogs-group
	//
	cwl.logNamespace = conf.GetString("ecs.log.namespace")
	if _, ok := confLogOptions["awslogs-group"]; ok && len(cwl.logNamespace) == 0 {
		cwl.logNamespace = confLogOptions["awslogs-group"]
	}

	if len(cwl.logNamespace) == 0 {
		return errors.Errorf(
			"ECSCloudWatchLogsClient needs one of [ecs.log.driver.options.awslogs-group] or [ecs.log.namespace] set in config")
	}

	cwl.logStreamPrefix = confLogOptions["awslogs-stream-prefix"]
	if len(cwl.logStreamPrefix) == 0 {
		return errors.Errorf("ECSCloudWatchLogsClient needs [ecs.log.driver.options.awslogs-stream-prefix] set in config")
	}

	cwl.logRetentionInDays = int64(conf.GetInt("ecs.log.retention_days"))
	if cwl.logRetentionInDays == 0 {
		cwl.logRetentionInDays = int64(30)
	}

	flotillaMode := conf.GetString("flotilla_mode")
	if flotillaMode != "test" {
		sess := session.Must(session.NewSession(&aws.Config{
			Region: aws.String(awsRegion)}))

		cwl.logsClient = cloudwatchlogs.New(sess)
	}
	cwl.logger = log.New(os.Stderr, "[ecscloudwatchlogs] ",
		log.Ldate|log.Ltime|log.Lshortfile)
	return cwl.createNamespaceIfNotExists()
}

//
// Logs returns all logs from the log stream identified by handle since lastSeen
//
func (cwl *ECSCloudWatchLogsClient) Logs(executable state.Executable, run state.Run, lastSeen *string) (string, *string, error) {
	startFromHead := true
	handle := cwl.toStreamName(executable, run)
	args := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  &cwl.logNamespace,
		LogStreamName: &handle,
		StartFromHead: &startFromHead,
	}

	if lastSeen != nil && len(*lastSeen) > 0 {
		args.NextToken = lastSeen
	}

	result, err := cwl.logsClient.GetLogEvents(args)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == cloudwatchlogs.ErrCodeResourceNotFoundException {
				return "", nil, exceptions.MissingResource{err.Error()}
			} else if request.IsErrorThrottle(err) {
				cwl.logger.Printf(
					"thottled getting logs; executable_id: %v, run_id: %s, error: %+v\n",
					executable.GetExecutableID(), run.RunID, err)
				return "", lastSeen, nil
			}
		}
		return "", nil, errors.Wrap(err, "problem getting logs")
	}

	if len(result.Events) == 0 {
		return "", result.NextForwardToken, nil
	}

	message := cwl.logsToMessage(result.Events)
	return message, result.NextForwardToken, nil
}

func (cwl *ECSCloudWatchLogsClient) LogsText(executable state.Executable, run state.Run, w http.ResponseWriter) error {
	return errors.Errorf("ECSCloudWatchLogsClient does not support LogsText method.")
}

func (cwl *ECSCloudWatchLogsClient) toStreamName(executable state.Executable, run state.Run) string {
	arnSplits := strings.Split(run.TaskArn, "/")
	return fmt.Sprintf(
		"%s/%s/%s", cwl.logStreamPrefix, executable.GetExecutableResources().ContainerName, arnSplits[len(arnSplits)-1])
}

func (cwl *ECSCloudWatchLogsClient) logsToMessage(events []*cloudwatchlogs.OutputLogEvent) string {
	sort.Sort(byTimestamp(events))

	messages := make([]string, len(events))
	for i, event := range events {
		messages[i] = *event.Message
	}
	return strings.Join(messages, "\n")
}

func (cwl *ECSCloudWatchLogsClient) createNamespaceIfNotExists() error {
	exists, err := cwl.namespaceExists()
	if err != nil {
		return errors.Wrapf(err, "problem checking if log namespace [%s] exists", cwl.logNamespace)
	}
	if !exists {
		return cwl.createNamespace()
	}
	return nil
}

func (cwl *ECSCloudWatchLogsClient) namespaceExists() (bool, error) {
	result, err := cwl.logsClient.DescribeLogGroups(&cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: &cwl.logNamespace,
	})

	if err != nil {
		return false, errors.Wrapf(err, "problem describing log groups with prefix [%s]", cwl.logNamespace)
	}
	if len(result.LogGroups) == 0 {
		return false, nil
	}
	for _, group := range result.LogGroups {
		if *group.LogGroupName == cwl.logNamespace {
			return true, nil
		}
	}
	return false, nil
}

func (cwl *ECSCloudWatchLogsClient) createNamespace() error {
	_, err := cwl.logsClient.CreateLogGroup(&cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: &cwl.logNamespace,
	})
	if err != nil {
		return errors.Wrapf(err, "problem creating log group with log group name [%s]", cwl.logNamespace)
	}

	_, err = cwl.logsClient.PutRetentionPolicy(&cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    &cwl.logNamespace,
		RetentionInDays: &cwl.logRetentionInDays,
	})
	if err != nil {
		return errors.Wrapf(err, "problem setting log group retention policy for log group name [%s]", cwl.logNamespace)
	}
	return nil
}
