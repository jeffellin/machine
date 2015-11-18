package amazoncf
/**
Todo
 * Copy the SSH Key to the machine folder
 * Allow specification of SSH USer
 * Allow use of public ip (Currently private is the Default)
 * Check for anything special related to swarm
 * Pass additional Paramaters to the CloudFormation
**/

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/state"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/ec2"
)

var (
	dockerPort = 2376
	swarmPort  = 3376
)

/*
 * This Driver will utilize a cloud formation stack to create an instance
*/
const driverName = "amazoncf"

type Driver struct {
	*drivers.BaseDriver
	Id                string
	CloudFormationURL string
	SSHKeyPath        string
	InstanceId        string
	PrivateIPAddress  string
	KeyPairName       string
}

func NewDriver(hostName, storePath string) *Driver {
	id := generateId()
	return &Driver{
		Id: id,
		BaseDriver: &drivers.BaseDriver{
			MachineName: hostName,
			StorePath:   storePath,
		},
	}
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			Name:  "cloudformation-url",
			Usage: "S3 URL of the CloudFormation File",
		},
		mcnflag.StringFlag{
			Name:  "cloudformation-keypairname",
			Usage: "SSH KeyPair to use",
		},
		mcnflag.StringFlag{
			Name:  "cloudformation-keypath",
			Usage: "keypath to SSH Private Key",
		},
	}
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	d.CloudFormationURL = flags.String("cloudformation-url")
	d.SSHKeyPath = flags.String("cloudformation-keypairname")
	d.KeyPairName = flags.String("cloudformation-keypath")
	return nil
}

func (d *Driver) DriverName() string {
	return driverName
}

func (d *Driver) PreCreateCheck() error {
	//nothing to check at the moment
	return nil
}

func (d *Driver) Create() error {

	svc := cloudformation.New(session.New())

	params := &cloudformation.CreateStackInput{
		StackName: aws.String(d.MachineName),
		TemplateURL: aws.String(d.CloudFormationURL),
		Parameters: []*cloudformation.Parameter{
			{ 
				ParameterKey:   aws.String("KeyName"),
				ParameterValue: aws.String(d.KeyPairName),
			},
		},
	}
	_, err := svc.CreateStack(params)
	//might want to log the resp

	if err != nil {
		fmt.Println("Houston we have a problem")
		fmt.Println(err.Error())
		return err
	} 

	if err := mcnutils.WaitFor(d.stackAvailable);err!=nil{
		return err
	}

	if err:=d.getInstanceInfo() ; err!=nil{
		log.Debug(err)
	}

	log.Debugf("created instance ID %s, IP address %s, Private IP address %s",
		d.InstanceId,
		d.IPAddress,
		d.PrivateIPAddress,
	)

	return nil
}

func (d *Driver) stackAvailable() bool {

	svc := cloudformation.New(session.New())

	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(d.MachineName),
	}
	resp, err := svc.DescribeStacks(params)

	if err != nil {
		log.Infof("Houston we have a problem")
		log.Infof(err.Error())
		return false
	}
	if *resp.Stacks[0].StackStatus == cloudformation.ResourceStatusCreateComplete {
		return true
	} else {
		log.Infof("...Stack Not Available Yet")
		return false
	}
}

/*
handle error,  we are passing it up now
*/
func (d *Driver) getInstanceInfo() error {

	svc := cloudformation.New(session.New())

	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(d.MachineName),
	}
	resp, _ := svc.DescribeStacks(params)

	for _, element := range resp.Stacks[0].Outputs {
		outputV := *element.OutputValue
		if *element.OutputKey == "PrivateIp" {
			d.PrivateIPAddress = outputV
		}
		if *element.OutputKey == "InstanceID" {
			d.InstanceId = outputV
		}
		if *element.OutputKey == "IpAddress" {
			d.IPAddress = outputV
		}

	}

	//get InstanceId, IpAddress, PrivateIpAddress

	return nil
}

func (d *Driver) GetURL() (string, error) {

	//use the IP to get a formatted url

	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	if ip == "" {
		return "", nil
	}
	return fmt.Sprintf("tcp://%s:%d", ip, dockerPort), nil
}

func (d *Driver) GetIP() (string, error) {

	return *d.getInstance().PrivateIpAddress, nil
}

func (d *Driver) getInstance() ec2.Instance {
	svc := ec2.New(session.New())

	params := &ec2.DescribeInstancesInput{
		//   DryRun: aws.Bool(true),i-65e27fce  9f2dea3d

		InstanceIds: []*string{
			aws.String(d.InstanceId), // Required
			// More values...
		},
		// MaxResults: aws.Int64(1),
		// NextToken:  aws.String("String"),
	}

	resp, err := svc.DescribeInstances(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())

	}

	//this should return error
	return *resp.Reservations[0].Instances[0]

}

func (d *Driver) GetState() (state.State, error) {

	//TODOO use EC2 instance info to get IP
	//handle error
	//inst := d.getInstance()
	//switch inst.State.Name {
	//case "pending":
	//	return state.Starting, nil
	//case "running":
	//	return state.Running, nil
	//case "stopping":
	//	return state.Stopping, nil
	//case "shutting-down":
	//	return state.Stopping, nil
	//case "stopped":
	//	return state.Stopped, nil
	//default:
	//	return state.Error, nil
	//}
	return state.Running, nil
}

// GetSSHHostname -
func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetSSHUsername() string {
	//TODOO implement variable for SSHUSER

	if d.SSHUser == "" {
		d.SSHUser = "ubuntu"
	}
	return d.SSHUser
}

func (d *Driver) Start() error {

	svc := ec2.New(session.New())

	params := &ec2.StartInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	resp, err := svc.StartInstances(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return err
	}

	// Pretty-print the response data.
	fmt.Println(resp)

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) waitForInstance() error {

	//need to wait on instance to start
	return nil
}

func (d *Driver) Restart() error {

	svc := ec2.New(session.New())

	params := &ec2.RebootInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	resp, err := svc.RebootInstances(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return err
	}

	// Pretty-print the response data.
	fmt.Println(resp)

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Kill() error {

	svc := ec2.New(session.New())

	params := &ec2.StopInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	resp, err := svc.StopInstances(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return err
	}

	// Pretty-print the response data.
	fmt.Println(resp)

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Stop() error {

	svc := ec2.New(session.New())

	params := &ec2.StopInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	resp, err := svc.StopInstances(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return err
	}

	// Pretty-print the response data.
	fmt.Println(resp)

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Remove() error {

	svc := cloudformation.New(session.New())

	params := &cloudformation.DeleteStackInput{
		StackName: aws.String(d.MachineName), // Required
	}
	resp, err := svc.DeleteStack(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		//return
	}

	// Pretty-print the response data.
	fmt.Println(resp)

	return nil
}

func generateId() string {
	rb := make([]byte, 10)
	_, err := rand.Read(rb)
	if err != nil {
		log.Warnf("Unable to generate id: %s", err)
	}

	h := md5.New()
	io.WriteString(h, string(rb))
	return fmt.Sprintf("%x", h.Sum(nil))
}
