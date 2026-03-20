package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func getECSTask(svc *ecs.Client, clusterName, serviceName string) (string, string, error) {
	input := &ecs.ListTasksInput{
		Cluster:       aws.String(clusterName),
		ServiceName:   aws.String(serviceName),
		DesiredStatus: types.DesiredStatusRunning,
	}
	result, err := svc.ListTasks(context.TODO(), input)
	if err != nil || len(result.TaskArns) == 0 {
		return "", "", fmt.Errorf("no running tasks found for service %s", serviceName)
	}

    if len(result.TaskArns) == 0 {
        return "", "", fmt.Errorf("no running tasks found for service %s", serviceName)
    }

    // Randomly select a task ARN
    rand.Seed(time.Now().UnixNano())
    taskArn := result.TaskArns[rand.Intn(len(result.TaskArns))]

	describeInput := &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	}
	describeResult, err := svc.DescribeTasks(context.TODO(), describeInput)
	if err != nil || len(describeResult.Tasks) == 0 {
		return "", "", fmt.Errorf("could not describe the ECS task")
	}
	containerInstanceArn := describeResult.Tasks[0].ContainerInstanceArn
	return taskArn, *containerInstanceArn, nil
}

func getEC2InstanceID(svc *ecs.Client, clusterName, containerInstanceArn string) (string, error) {
	input := &ecs.DescribeContainerInstancesInput{
		Cluster:            aws.String(clusterName),
		ContainerInstances: []string{containerInstanceArn},
	}
	result, err := svc.DescribeContainerInstances(context.TODO(), input)
	if err != nil || len(result.ContainerInstances) == 0 {
		return "", fmt.Errorf("could not describe container instance")
	}

	if len(result.ContainerInstances) == 0 {
        return "", fmt.Errorf("no container instances found for cluster %s", clusterName)
    }

    rand.Seed(time.Now().UnixNano())
    selectedInstance := result.ContainerInstances[rand.Intn(len(result.ContainerInstances))]
    return *selectedInstance.Ec2InstanceId, nil
}

func getContainerID(svc *ecs.Client, clusterName, taskArn, containerName string) (string, error) {
	describeInput := &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	}
	describeResult, err := svc.DescribeTasks(context.TODO(), describeInput)
	if err != nil || len(describeResult.Tasks) == 0 {
		return "", fmt.Errorf("could not describe the ECS task")
	}
	for _, container := range describeResult.Tasks[0].Containers {
		if *container.Name == containerName {
			return *container.RuntimeId, nil
		}
	}
	return "", fmt.Errorf("no container named %s found in task", containerName)
}

func startSSMSession(instanceID, containerID string, profile *string, region string) error {
    ssmCmd := []string{
        "aws", "ssm", "start-session",
        "--target", instanceID,
        "--document-name", "AWS-StartInteractiveCommand",
        "--parameters", fmt.Sprintf(`{"command":["sudo docker exec -it %s bash"]}`, containerID),
        "--region", region,
    }

	if profile != nil {
		ssmCmd = append(ssmCmd, "--profile", *profile)
	}
	cmd := exec.Command(ssmCmd[0], ssmCmd[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func validateAWSCredentials(cfg aws.Config) error {
	stsClient := sts.NewFromConfig(cfg)

	identityOutput, err := stsClient.GetCallerIdentity(context.TODO(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("unable to authenticate with AWS, please check you are logged in")
	}

	fmt.Printf("Authenticated as ARN: %s (Account: %s, UserId: %s)\n",
		aws.ToString(identityOutput.Arn),
		aws.ToString(identityOutput.Account),
		aws.ToString(identityOutput.UserId))
	return nil
}

func main() {
	clusterName := flag.String("cluster", "", "The ECS cluster name")
	serviceName := flag.String("service", "", "The ECS service name")
	containerName := flag.String("container", "", "The container name")
	profile := flag.String("profile", "", "Optional AWS profile name")

	flag.Parse()

	if *serviceName == "" || *containerName == "" {
		log.Fatal("Usage: docker-connector --cluster <cluster-name> --service <service-name> --container <container-name> [--profile <aws-profile>]")
	}

	var cfg aws.Config
	var err error
	region := "eu-west-2"
	if *profile != "" {
		cfg, err = config.LoadDefaultConfig(context.TODO(), config.WithSharedConfigProfile(*profile), config.WithRegion(region))
	} else {
		cfg, err = config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	}
	if err != nil {
		log.Fatalf("Unable to load AWS config: %v", err)
	}

	if err := validateAWSCredentials(cfg); err != nil {
		log.Fatalf("AWS authentication failed: %v", err)
	}

	ecsClient := ecs.NewFromConfig(cfg)

	maxRetries := 3
	retrySuccess := false
	backoffDelay := time.Second * 5

	for i := 0; i < maxRetries; i++ {
		taskArn, containerInstanceArn, err := getECSTask(ecsClient, *clusterName, *serviceName)
		if err != nil {
			if i == maxRetries-1 {
				log.Fatalf("Error getting ECS task: %v. Maximum retries reached.", err)
			} else {
				log.Printf("No running tasks found for service. Retrying in %v...", backoffDelay)
				time.Sleep(backoffDelay)
				continue
			}
		}

		log.Printf("Found task ARN: %s\n", taskArn)

		instanceID, err := getEC2InstanceID(ecsClient, *clusterName, containerInstanceArn)
		if err != nil {
			if i == maxRetries-1 {
				log.Fatalf("Error getting EC2 instance ID: %v. Maximum retries reached.", err)
			} else {
				log.Printf("Error getting EC2 instance ID. Retrying in %v...", backoffDelay)
				time.Sleep(backoffDelay)
				continue
			}
		}

		log.Printf("Found EC2 instance ID: %s\n", instanceID)

		containerID, err := getContainerID(ecsClient, *clusterName, taskArn, *containerName)
		if err != nil {
			if i == maxRetries-1 {
				log.Fatalf("Error getting container ID: %v. Maximum retries reached.", err)
			} else {
				log.Printf("Error getting container ID. Retrying in %v...", backoffDelay)
				time.Sleep(backoffDelay)
				continue
			}
		}

		log.Printf("Found container ID: %s\n", containerID)

		log.Printf("Attempting to start SSM session (Attempt %d/%d)...", i+1, maxRetries)
		err = startSSMSession(instanceID, containerID, profile, region)
		if err == nil {
			log.Println("SSM session started successfully.")
			retrySuccess = true
			break
		}

		log.Printf("Failed to start SSM session: %v", err)

		if i < maxRetries-1 {
			log.Printf("Retrying with a new ECS task and container instance in %v...", backoffDelay)
			time.Sleep(backoffDelay)
		}
	}

	if !retrySuccess {
		log.Fatalf("Failed to start SSM session after %d attempts.", maxRetries)
	}
}
