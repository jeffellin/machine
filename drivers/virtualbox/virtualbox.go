package virtualbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/state"
)

const (
	defaultCPU                 = 1
	defaultMemory              = 1024
	defaultBoot2DockerURL      = ""
	defaultBoot2DockerImportVM = ""
	defaultHostOnlyCIDR        = "192.168.99.1/24"
	defaultHostOnlyNictype     = "82540EM"
	defaultHostOnlyPromiscMode = "deny"
	defaultDiskSize            = 20000
)

var (
	ErrUnableToGenerateRandomIP = errors.New("unable to generate random IP")
	ErrMustEnableVTX            = errors.New("This computer doesn't have VT-X/AMD-v enabled. Enabling it in the BIOS is mandatory")
	ErrNotCompatibleWithHyperV  = errors.New("This computer has Hyper-V installed. VirtualBox refuses to boot a 64bits VM when Hyper-V is installed. See https://www.virtualbox.org/ticket/12350")
	ErrNetworkAddrCidr          = errors.New("host-only cidr must be specified with a host address, not a network address")
)

type Driver struct {
	*drivers.BaseDriver
	VBoxManager
	b2dUpdater          B2DUpdater
	sshKeyGenerator     SSHKeyGenerator
	diskCreator         DiskCreator
	logsReader          LogsReader
	ipWaiter            IPWaiter
	randomInter         RandomInter
	sleeper             Sleeper
	CPU                 int
	Memory              int
	DiskSize            int
	Boot2DockerURL      string
	Boot2DockerImportVM string
	HostDNSResolver     bool
	HostOnlyCIDR        string
	HostOnlyNicType     string
	HostOnlyPromiscMode string
	NoShare             bool
	DNSProxy            bool
	NoVTXCheck          bool
}

// NewDriver creates a new VirtualBox driver with default settings.
func NewDriver(hostName, storePath string) *Driver {
	return &Driver{
		VBoxManager:         NewVBoxManager(),
		b2dUpdater:          NewB2DUpdater(),
		sshKeyGenerator:     NewSSHKeyGenerator(),
		diskCreator:         NewDiskCreator(),
		logsReader:          NewLogsReader(),
		ipWaiter:            NewIPWaiter(),
		randomInter:         NewRandomInter(),
		sleeper:             NewSleeper(),
		Memory:              defaultMemory,
		CPU:                 defaultCPU,
		DiskSize:            defaultDiskSize,
		HostOnlyCIDR:        defaultHostOnlyCIDR,
		HostOnlyNicType:     defaultHostOnlyNictype,
		HostOnlyPromiscMode: defaultHostOnlyPromiscMode,
		BaseDriver: &drivers.BaseDriver{
			MachineName: hostName,
			StorePath:   storePath,
		},
	}
}

// GetCreateFlags registers the flags this driver adds to
// "docker hosts create"
func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.IntFlag{
			Name:   "virtualbox-memory",
			Usage:  "Size of memory for host in MB",
			Value:  defaultMemory,
			EnvVar: "VIRTUALBOX_MEMORY_SIZE",
		},
		mcnflag.IntFlag{
			Name:   "virtualbox-cpu-count",
			Usage:  "number of CPUs for the machine (-1 to use the number of CPUs available)",
			Value:  defaultCPU,
			EnvVar: "VIRTUALBOX_CPU_COUNT",
		},
		mcnflag.IntFlag{
			Name:   "virtualbox-disk-size",
			Usage:  "Size of disk for host in MB",
			Value:  defaultDiskSize,
			EnvVar: "VIRTUALBOX_DISK_SIZE",
		},
		mcnflag.StringFlag{
			Name:   "virtualbox-boot2docker-url",
			Usage:  "The URL of the boot2docker image. Defaults to the latest available version",
			Value:  defaultBoot2DockerURL,
			EnvVar: "VIRTUALBOX_BOOT2DOCKER_URL",
		},
		mcnflag.StringFlag{
			Name:   "virtualbox-import-boot2docker-vm",
			Usage:  "The name of a Boot2Docker VM to import",
			Value:  defaultBoot2DockerImportVM,
			EnvVar: "VIRTUALBOX_BOOT2DOCKER_IMPORT_VM",
		},
		mcnflag.BoolFlag{
			Name:   "virtualbox-host-dns-resolver",
			Usage:  "Use the host DNS resolver",
			EnvVar: "VIRTUALBOX_HOST_DNS_RESOLVER",
		},
		mcnflag.StringFlag{
			Name:   "virtualbox-hostonly-cidr",
			Usage:  "Specify the Host Only CIDR",
			Value:  defaultHostOnlyCIDR,
			EnvVar: "VIRTUALBOX_HOSTONLY_CIDR",
		},
		mcnflag.StringFlag{
			Name:   "virtualbox-hostonly-nictype",
			Usage:  "Specify the Host Only Network Adapter Type",
			Value:  defaultHostOnlyNictype,
			EnvVar: "VIRTUALBOX_HOSTONLY_NIC_TYPE",
		},
		mcnflag.StringFlag{
			Name:   "virtualbox-hostonly-nicpromisc",
			Usage:  "Specify the Host Only Network Adapter Promiscuous Mode",
			Value:  defaultHostOnlyPromiscMode,
			EnvVar: "VIRTUALBOX_HOSTONLY_NIC_PROMISC",
		},
		mcnflag.BoolFlag{
			Name:   "virtualbox-no-share",
			Usage:  "Disable the mount of your home directory",
			EnvVar: "VIRTUALBOX_NO_SHARE",
		},
		mcnflag.BoolFlag{
			Name:   "virtualbox-dns-proxy",
			Usage:  "Proxy all DNS requests to the host",
			EnvVar: "VIRTUALBOX_DNS_PROXY",
		},
		mcnflag.BoolFlag{
			Name:   "virtualbox-no-vtx-check",
			Usage:  "Disable checking for the availability of hardware virtualization before the vm is started",
			EnvVar: "VIRTUALBOX_NO_VTX_CHECK",
		},
	}
}

func (d *Driver) GetSSHHostname() (string, error) {
	return "127.0.0.1", nil
}

func (d *Driver) GetSSHUsername() string {
	if d.SSHUser == "" {
		d.SSHUser = "docker"
	}

	return d.SSHUser
}

// DriverName returns the name of the driver
func (d *Driver) DriverName() string {
	return "virtualbox"
}

func (d *Driver) GetURL() (string, error) {
	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	if ip == "" {
		return "", nil
	}
	return fmt.Sprintf("tcp://%s:2376", ip), nil
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	d.CPU = flags.Int("virtualbox-cpu-count")
	d.Memory = flags.Int("virtualbox-memory")
	d.DiskSize = flags.Int("virtualbox-disk-size")
	d.Boot2DockerURL = flags.String("virtualbox-boot2docker-url")
	d.SetSwarmConfigFromFlags(flags)
	d.SSHUser = "docker"
	d.Boot2DockerImportVM = flags.String("virtualbox-import-boot2docker-vm")
	d.HostDNSResolver = flags.Bool("virtualbox-host-dns-resolver")
	d.HostOnlyCIDR = flags.String("virtualbox-hostonly-cidr")
	d.HostOnlyNicType = flags.String("virtualbox-hostonly-nictype")
	d.HostOnlyPromiscMode = flags.String("virtualbox-hostonly-nicpromisc")
	d.NoShare = flags.Bool("virtualbox-no-share")
	d.DNSProxy = flags.Bool("virtualbox-dns-proxy")
	d.NoVTXCheck = flags.Bool("virtualbox-no-vtx-check")

	return nil
}

// PreCreateCheck checks that VBoxManage exists and works
func (d *Driver) PreCreateCheck() error {
	// Check that VBoxManage exists and works
	version, err := d.vbmOut("--version")
	if err != nil {
		return err
	}

	// Check that VBoxManage is of a supported version
	if err = checkVBoxManageVersion(strings.TrimSpace(version)); err != nil {
		return err
	}

	if !d.NoVTXCheck {
		if isHyperVInstalled() {
			return ErrNotCompatibleWithHyperV
		}

		if d.IsVTXDisabled() {
			return ErrMustEnableVTX
		}
	}

	// Downloading boot2docker to cache should be done here to make sure
	// that a download failure will not leave a machine half created.
	if err := d.b2dUpdater.UpdateISOCache(d.StorePath, d.Boot2DockerURL); err != nil {
		return err
	}

	// Check that Host-only interfaces are ok
	if _, err = listHostOnlyAdapters(d.VBoxManager); err != nil {
		return err
	}

	return nil
}

// IsVTXDisabledInTheVM checks if VT-X is disabled in the started vm.
func (d *Driver) IsVTXDisabledInTheVM() (bool, error) {
	logPath := filepath.Join(d.ResolveStorePath(d.MachineName), "Logs", "VBox.log")
	log.Debugf("Checking vm logs: %s", logPath)

	lines, err := d.logsReader.Read(logPath)
	if err != nil {
		return true, err
	}

	for _, line := range lines {
		if strings.Contains(line, "VT-x is disabled") {
			return true, nil
		}
		if strings.Contains(line, "the host CPU does NOT support HW virtualization") {
			return true, nil
		}
		if strings.Contains(line, "VERR_VMX_UNABLE_TO_START_VM") {
			return true, nil
		}
	}

	return false, nil
}

func (d *Driver) Create() error {
	if err := d.CreateVM(); err != nil {
		return err
	}

	log.Info("Starting the VM...")
	return d.Start()
}

func (d *Driver) CreateVM() error {
	if err := d.b2dUpdater.CopyIsoToMachineDir(d.StorePath, d.MachineName, d.Boot2DockerURL); err != nil {
		return err
	}

	log.Info("Creating VirtualBox VM...")

	// import b2d VM if requested
	if d.Boot2DockerImportVM != "" {
		name := d.Boot2DockerImportVM

		// make sure vm is stopped
		_ = d.vbm("controlvm", name, "poweroff")

		diskInfo, err := getVMDiskInfo(name, d.VBoxManager)
		if err != nil {
			return err
		}

		if _, err := os.Stat(diskInfo.Path); err != nil {
			return err
		}

		if err := d.vbm("clonehd", diskInfo.Path, d.diskPath()); err != nil {
			return err
		}

		log.Debugf("Importing VM settings...")
		vmInfo, err := getVMInfo(name, d.VBoxManager)
		if err != nil {
			return err
		}

		d.CPU = vmInfo.CPUs
		d.Memory = vmInfo.Memory

		log.Debugf("Importing SSH key...")
		keyPath := filepath.Join(mcnutils.GetHomeDir(), ".ssh", "id_boot2docker")
		if err := mcnutils.CopyFile(keyPath, d.GetSSHKeyPath()); err != nil {
			return err
		}
	} else {
		log.Infof("Creating SSH key...")
		if err := d.sshKeyGenerator.Generate(d.GetSSHKeyPath()); err != nil {
			return err
		}

		log.Debugf("Creating disk image...")
		if err := d.diskCreator.Create(d.DiskSize, d.publicSSHKeyPath(), d.diskPath()); err != nil {
			return err
		}
	}

	if err := d.vbm("createvm",
		"--basefolder", d.ResolveStorePath("."),
		"--name", d.MachineName,
		"--register"); err != nil {
		return err
	}

	log.Debugf("VM CPUS: %d", d.CPU)
	log.Debugf("VM Memory: %d", d.Memory)

	cpus := d.CPU
	if cpus < 1 {
		cpus = int(runtime.NumCPU())
	}
	if cpus > 32 {
		cpus = 32
	}

	hostDNSResolver := "off"
	if d.HostDNSResolver {
		hostDNSResolver = "on"
	}

	dnsProxy := "off"
	if d.DNSProxy {
		dnsProxy = "on"
	}

	if err := d.vbm("modifyvm", d.MachineName,
		"--firmware", "bios",
		"--bioslogofadein", "off",
		"--bioslogofadeout", "off",
		"--bioslogodisplaytime", "0",
		"--biosbootmenu", "disabled",
		"--ostype", "Linux26_64",
		"--cpus", fmt.Sprintf("%d", cpus),
		"--memory", fmt.Sprintf("%d", d.Memory),
		"--acpi", "on",
		"--ioapic", "on",
		"--rtcuseutc", "on",
		"--natdnshostresolver1", hostDNSResolver,
		"--natdnsproxy1", dnsProxy,
		"--cpuhotplug", "off",
		"--pae", "on",
		"--hpet", "on",
		"--hwvirtex", "on",
		"--nestedpaging", "on",
		"--largepages", "on",
		"--vtxvpid", "on",
		"--accelerate3d", "off",
		"--boot1", "dvd"); err != nil {
		return err
	}

	if err := d.vbm("modifyvm", d.MachineName,
		"--nic1", "nat",
		"--nictype1", "82540EM",
		"--cableconnected1", "on"); err != nil {
		return err
	}

	if err := d.vbm("storagectl", d.MachineName,
		"--name", "SATA",
		"--add", "sata",
		"--hostiocache", "on"); err != nil {
		return err
	}

	if err := d.vbm("storageattach", d.MachineName,
		"--storagectl", "SATA",
		"--port", "0",
		"--device", "0",
		"--type", "dvddrive",
		"--medium", d.ResolveStorePath("boot2docker.iso")); err != nil {
		return err
	}

	if err := d.vbm("storageattach", d.MachineName,
		"--storagectl", "SATA",
		"--port", "1",
		"--device", "0",
		"--type", "hdd",
		"--medium", d.diskPath()); err != nil {
		return err
	}

	// let VBoxService do nice magic automounting (when it's used)
	if err := d.vbm("guestproperty", "set", d.MachineName, "/VirtualBox/GuestAdd/SharedFolders/MountPrefix", "/"); err != nil {
		return err
	}
	if err := d.vbm("guestproperty", "set", d.MachineName, "/VirtualBox/GuestAdd/SharedFolders/MountDir", "/"); err != nil {
		return err
	}

	shareName, shareDir := getShareDriveAndName()

	if shareDir != "" && !d.NoShare {
		log.Debugf("setting up shareDir")
		if _, err := os.Stat(shareDir); err != nil && !os.IsNotExist(err) {
			return err
		} else if !os.IsNotExist(err) {
			if shareName == "" {
				// parts of the VBox internal code are buggy with share names that start with "/"
				shareName = strings.TrimLeft(shareDir, "/")
				// TODO do some basic Windows -> MSYS path conversion
				// ie, s!^([a-z]+):[/\\]+!\1/!; s!\\!/!g
			}

			// woo, shareDir exists!  let's carry on!
			if err := d.vbm("sharedfolder", "add", d.MachineName, "--name", shareName, "--hostpath", shareDir, "--automount"); err != nil {
				return err
			}

			// enable symlinks
			if err := d.vbm("setextradata", d.MachineName, "VBoxInternal2/SharedFoldersEnableSymlinksCreate/"+shareName, "1"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *Driver) hostOnlyIPAvailable() bool {
	ip, err := d.GetIP()
	if err != nil {
		log.Debugf("ERROR getting IP: %s", err)
		return false
	}
	if ip == "" {
		log.Debug("Strangely, there was no error attempting to get the IP, but it was still empty.")
		return false
	}

	log.Debugf("IP is %s", ip)
	return true
}

func (d *Driver) Start() error {
	s, err := d.GetState()
	if err != nil {
		return err
	}

	var hostOnlyAdapter *hostOnlyNetwork
	if s == state.Stopped {
		// check network to re-create if needed
		if hostOnlyAdapter, err = d.setupHostOnlyNetwork(d.MachineName); err != nil {
			return fmt.Errorf("Error setting up host only network on machine start: %s", err)
		}
	}

	switch s {
	case state.Stopped, state.Saved:
		d.SSHPort, err = setPortForwarding(d, 1, "ssh", "tcp", 22, d.SSHPort)
		if err != nil {
			return err
		}

		if err := d.vbm("startvm", d.MachineName, "--type", "headless"); err != nil {
			// TODO: We could capture the last lines of the vbox log
			return fmt.Errorf("Unable to start the VM: %s", err)
		}
	case state.Paused:
		if err := d.vbm("controlvm", d.MachineName, "resume", "--type", "headless"); err != nil {
			return err
		}
		log.Infof("Resuming VM ...")
	default:
		log.Infof("VM not in restartable state")
	}

	// Verify that VT-X is not disabled in the started VM
	vtxIsDisabled, err := d.IsVTXDisabledInTheVM()
	if err != nil {
		return fmt.Errorf("Checking if hardware virtualization is enabled failed: %s", err)
	}

	if vtxIsDisabled {
		return ErrMustEnableVTX
	}

	log.Infof("Waiting for an IP...")
	if err := d.ipWaiter.Wait(d); err != nil {
		return err
	}

	if hostOnlyAdapter == nil {
		return nil
	}

	// Check that the host-only adapter we just created can still be found
	// Sometimes it is corrupted after the VM is started.
	nets, err := listHostOnlyAdapters(d.VBoxManager)
	if err != nil {
		return err
	}

	ip, network, err := parseAndValidateCIDR(d.HostOnlyCIDR)
	if err != nil {
		return err
	}

	hostOnlyNet := getHostOnlyAdapter(nets, ip, network.Mask)
	if hostOnlyNet != nil {
		// OK, we found a valid host-only adapter
		return nil
	}

	// This happens a lot on windows. The adapter has an invalid IP and the VM has the same IP
	log.Warn("The host-only adapter is corrupted. Let's stop the VM, fix the host-only adapter and restart the VM")
	if err := d.Stop(); err != nil {
		return err
	}

	// We have to be sure the host-only adapter is not used by the VM
	d.sleeper.Sleep(5 * time.Second)

	log.Debugf("Fixing %+v...", hostOnlyAdapter)
	if err := hostOnlyAdapter.SaveIPv4(d.VBoxManager); err != nil {
		return err
	}

	// We have to be sure the adapter is updated before starting the VM
	d.sleeper.Sleep(5 * time.Second)

	if err := d.vbm("startvm", d.MachineName, "--type", "headless"); err != nil {
		return fmt.Errorf("Unable to start the VM: %s", err)
	}

	log.Infof("Waiting for an IP...")
	return d.ipWaiter.Wait(d)
}

func (d *Driver) Stop() error {
	currentState, err := d.GetState()
	if err != nil {
		return err
	}

	if currentState == state.Paused {
		if err := d.vbm("controlvm", d.MachineName, "resume"); err != nil { // , "--type", "headless"
			return err
		}
		log.Infof("Resuming VM ...")
	}

	if err := d.vbm("controlvm", d.MachineName, "acpipowerbutton"); err != nil {
		return err
	}
	for {
		s, err := d.GetState()
		if err != nil {
			return err
		}
		if s == state.Running {
			d.sleeper.Sleep(1 * time.Second)
		} else {
			break
		}
	}

	d.IPAddress = ""

	return nil
}

// Restart restarts a machine which is known to be running.
func (d *Driver) Restart() error {
	if err := d.vbm("controlvm", d.MachineName, "reset"); err != nil {
		return err
	}

	d.IPAddress = ""

	return d.ipWaiter.Wait(d)
}

func (d *Driver) Kill() error {
	return d.vbm("controlvm", d.MachineName, "poweroff")
}

func (d *Driver) Remove() error {
	s, err := d.GetState()
	if err != nil {
		if err == ErrMachineNotExist {
			log.Infof("machine does not exist, assuming it has been removed already")
			return nil
		}
		return err
	}
	if s == state.Running {
		if err := d.Stop(); err != nil {
			return err
		}
	} else if s != state.Stopped {
		if err := d.Kill(); err != nil {
			return err
		}
	}
	// vbox will not release it's lock immediately after the stop
	d.sleeper.Sleep(1 * time.Second)
	return d.vbm("unregistervm", "--delete", d.MachineName)
}

func (d *Driver) GetState() (state.State, error) {
	stdout, stderr, err := d.vbmOutErr("showvminfo", d.MachineName, "--machinereadable")
	if err != nil {
		if reMachineNotFound.FindString(stderr) != "" {
			return state.Error, ErrMachineNotExist
		}
		return state.Error, err
	}
	re := regexp.MustCompile(`(?m)^VMState="(\w+)"`)
	groups := re.FindStringSubmatch(stdout)
	if len(groups) < 1 {
		return state.None, nil
	}
	switch groups[1] {
	case "running":
		return state.Running, nil
	case "paused":
		return state.Paused, nil
	case "saved":
		return state.Saved, nil
	case "poweroff", "aborted":
		return state.Stopped, nil
	}
	return state.None, nil
}

func (d *Driver) GetIP() (string, error) {
	// DHCP is used to get the IP, so virtualbox hosts don't have IPs unless
	// they are running
	s, err := d.GetState()
	if err != nil {
		return "", err
	}
	if s != state.Running {
		return "", drivers.ErrHostIsNotRunning
	}

	output, err := drivers.RunSSHCommandFromDriver(d, "ip addr show dev eth1")
	if err != nil {
		return "", err
	}

	log.Debugf("SSH returned: %s\nEND SSH\n", output)

	// parse to find: inet 192.168.59.103/24 brd 192.168.59.255 scope global eth1
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		vals := strings.Split(strings.TrimSpace(line), " ")
		if len(vals) >= 2 && vals[0] == "inet" {
			return vals[1][:strings.Index(vals[1], "/")], nil
		}
	}

	return "", fmt.Errorf("No IP address found %s", output)
}

func (d *Driver) publicSSHKeyPath() string {
	return d.GetSSHKeyPath() + ".pub"
}

func (d *Driver) diskPath() string {
	return d.ResolveStorePath("disk.vmdk")
}

func (d *Driver) setupHostOnlyNetwork(machineName string) (*hostOnlyNetwork, error) {
	hostOnlyCIDR := d.HostOnlyCIDR

	// This is to assist in migrating from version 0.2 to 0.3 format
	// it should be removed in a later release
	if hostOnlyCIDR == "" {
		hostOnlyCIDR = defaultHostOnlyCIDR
	}

	ip, network, err := parseAndValidateCIDR(hostOnlyCIDR)
	if err != nil {
		return nil, err
	}

	log.Debugf("Searching for hostonly interface for IPv4: %s and Mask: %s", ip, network.Mask)
	hostOnlyAdapter, err := getOrCreateHostOnlyNetwork(ip, network.Mask, d.VBoxManager)
	if err != nil {
		return nil, err
	}

	log.Debug("Removing orphan DHCP servers...")
	if err := removeOrphanDHCPServers(d.VBoxManager); err != nil {
		return nil, err
	}

	dhcpAddr, err := getRandomIPinSubnet(d, ip)
	if err != nil {
		return nil, err
	}

	log.Debugf("Adding/Modifying DHCP server %q...", dhcpAddr)
	nAddr := network.IP.To4()

	dhcp := dhcpServer{}
	dhcp.IPv4.IP = dhcpAddr
	dhcp.IPv4.Mask = network.Mask
	dhcp.LowerIP = net.IPv4(nAddr[0], nAddr[1], nAddr[2], byte(100))
	dhcp.UpperIP = net.IPv4(nAddr[0], nAddr[1], nAddr[2], byte(254))
	dhcp.Enabled = true
	if err := addHostOnlyDHCPServer(hostOnlyAdapter.Name, dhcp, d.VBoxManager); err != nil {
		return nil, err
	}

	if err := d.vbm("modifyvm", machineName,
		"--nic2", "hostonly",
		"--nictype2", d.HostOnlyNicType,
		"--nicpromisc2", d.HostOnlyPromiscMode,
		"--hostonlyadapter2", hostOnlyAdapter.Name,
		"--cableconnected2", "on"); err != nil {
		return nil, err
	}

	return hostOnlyAdapter, nil
}

func parseAndValidateCIDR(hostOnlyCIDR string) (net.IP, *net.IPNet, error) {
	ip, network, err := net.ParseCIDR(hostOnlyCIDR)
	if err != nil {
		return nil, nil, err
	}

	networkAddress := network.IP.To4()
	if ip.Equal(networkAddress) {
		return nil, nil, ErrNetworkAddrCidr
	}

	return ip, network, nil
}

// Select an available port, trying the specified
// port first, falling back on an OS selected port.
func getAvailableTCPPort(port int) (int, error) {
	for i := 0; i <= 10; i++ {
		ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return 0, err
		}
		defer ln.Close()
		addr := ln.Addr().String()
		addrParts := strings.SplitN(addr, ":", 2)
		p, err := strconv.Atoi(addrParts[1])
		if err != nil {
			return 0, err
		}
		if p != 0 {
			port = p
			return port, nil
		}
		port = 0 // Throw away the port hint before trying again
		time.Sleep(1)
	}
	return 0, fmt.Errorf("unable to allocate tcp port")
}

// Setup a NAT port forwarding entry.
func setPortForwarding(d *Driver, interfaceNum int, mapName, protocol string, guestPort, desiredHostPort int) (int, error) {
	actualHostPort, err := getAvailableTCPPort(desiredHostPort)
	if err != nil {
		return -1, err
	}
	if desiredHostPort != actualHostPort && desiredHostPort != 0 {
		log.Debugf("NAT forwarding host port for guest port %d (%s) changed from %d to %d",
			guestPort, mapName, desiredHostPort, actualHostPort)
	}
	cmd := fmt.Sprintf("--natpf%d", interfaceNum)
	d.vbm("modifyvm", d.MachineName, cmd, "delete", mapName)
	if err := d.vbm("modifyvm", d.MachineName,
		cmd, fmt.Sprintf("%s,%s,127.0.0.1,%d,,%d", mapName, protocol, actualHostPort, guestPort)); err != nil {
		return -1, err
	}
	return actualHostPort, nil
}

// getRandomIPinSubnet returns a pseudo-random net.IP in the same
// subnet as the IP passed
func getRandomIPinSubnet(d *Driver, baseIP net.IP) (net.IP, error) {
	var dhcpAddr net.IP

	nAddr := baseIP.To4()
	// select pseudo-random DHCP addr; make sure not to clash with the host
	// only try 5 times and bail if no random received
	for i := 0; i < 5; i++ {
		n := d.randomInter.RandomInt(25)
		if byte(n) != nAddr[3] {
			dhcpAddr = net.IPv4(nAddr[0], nAddr[1], nAddr[2], byte(n))
			break
		}
	}

	if dhcpAddr == nil {
		return nil, ErrUnableToGenerateRandomIP
	}

	return dhcpAddr, nil
}

func detectVBoxManageCmdInPath() string {
	cmd := "VBoxManage"
	if path, err := exec.LookPath(cmd); err == nil {
		return path
	}
	return cmd
}
