package iso

import (
	"errors"
	"fmt"
	"github.com/mitchellh/multistep"
	parallelscommon "github.com/mitchellh/packer/builder/parallels/common"
	"github.com/mitchellh/packer/common"
	"github.com/mitchellh/packer/packer"
	winparallelscommon "github.com/packer-community/packer-windows-plugins/builder/parallels-windows/common"
	wincommon "github.com/packer-community/packer-windows-plugins/common"
	"log"
	"strings"
)

const BuilderId = "rickard-von-essen.parallels"

type Builder struct {
	config config
	runner multistep.Runner
}

type config struct {
	common.PackerConfig                 `mapstructure:",squash"`
	parallelscommon.FloppyConfig        `mapstructure:",squash"`
	parallelscommon.OutputConfig        `mapstructure:",squash"`
	parallelscommon.PrlctlConfig        `mapstructure:",squash"`
	parallelscommon.PrlctlVersionConfig `mapstructure:",squash"`
	parallelscommon.RunConfig           `mapstructure:",squash"`
	parallelscommon.ShutdownConfig      `mapstructure:",squash"`
	wincommon.WinRMConfig               `mapstructure:",squash"`
	parallelscommon.ToolsConfig         `mapstructure:",squash"`

	BootCommand        []string `mapstructure:"boot_command"`
	DiskSize           uint     `mapstructure:"disk_size"`
	GuestOSType        string   `mapstructure:"guest_os_type"`
	HardDriveInterface string   `mapstructure:"hard_drive_interface"`
	HostInterfaces     []string `mapstructure:"host_interfaces"`
	HTTPDir            string   `mapstructure:"http_directory"`
	HTTPPortMin        uint     `mapstructure:"http_port_min"`
	HTTPPortMax        uint     `mapstructure:"http_port_max"`
	ISOChecksum        string   `mapstructure:"iso_checksum"`
	ISOChecksumType    string   `mapstructure:"iso_checksum_type"`
	ISOUrls            []string `mapstructure:"iso_urls"`
	VMName             string   `mapstructure:"vm_name"`

	RawSingleISOUrl string `mapstructure:"iso_url"`

	// Deprecated parameters
	GuestOSDistribution    string `mapstructure:"guest_os_distribution"`
	ParallelsToolsHostPath string `mapstructure:"parallels_tools_host_path"`

	tpl *packer.ConfigTemplate
}

func (b *Builder) Prepare(raws ...interface{}) ([]string, error) {

	md, err := common.DecodeConfig(&b.config, raws...)
	if err != nil {
		return nil, err
	}

	b.config.tpl, err = packer.NewConfigTemplate()
	if err != nil {
		return nil, err
	}
	b.config.tpl.UserVars = b.config.PackerUserVars

	// Accumulate any errors and warnings
	errs := common.CheckUnusedConfig(md)
	errs = packer.MultiErrorAppend(errs, b.config.FloppyConfig.Prepare(b.config.tpl)...)
	errs = packer.MultiErrorAppend(
		errs, b.config.OutputConfig.Prepare(b.config.tpl, &b.config.PackerConfig)...)
	errs = packer.MultiErrorAppend(errs, b.config.RunConfig.Prepare(b.config.tpl)...)
	errs = packer.MultiErrorAppend(errs, b.config.PrlctlConfig.Prepare(b.config.tpl)...)
	errs = packer.MultiErrorAppend(errs, b.config.PrlctlVersionConfig.Prepare(b.config.tpl)...)
	errs = packer.MultiErrorAppend(errs, b.config.ShutdownConfig.Prepare(b.config.tpl)...)
	errs = packer.MultiErrorAppend(errs, b.config.WinRMConfig.Prepare(b.config.tpl)...)
	errs = packer.MultiErrorAppend(errs, b.config.ToolsConfig.Prepare(b.config.tpl)...)
	warnings := make([]string, 0)

	if b.config.DiskSize == 0 {
		b.config.DiskSize = 40000
	}

	if b.config.HardDriveInterface == "" {
		b.config.HardDriveInterface = "sata"
	}

	if b.config.GuestOSType == "" {
		b.config.GuestOSType = "other"
	}

	if b.config.GuestOSDistribution != "" {
		// Compatibility with older templates:
		// Use value of 'guest_os_distribution' if it is defined.
		b.config.GuestOSType = b.config.GuestOSDistribution
		warnings = append(warnings,
			"A 'guest_os_distribution' has been completely replaced with 'guest_os_type'\n"+
				"It is recommended to remove it and assign the previous value to 'guest_os_type'.\n"+
				"Run it to see all available values: `prlctl create x -d list` ")
	}

	if b.config.HTTPPortMin == 0 {
		b.config.HTTPPortMin = 8000
	}

	if b.config.HTTPPortMax == 0 {
		b.config.HTTPPortMax = 9000
	}

	if len(b.config.HostInterfaces) == 0 {
		b.config.HostInterfaces = []string{"en0", "en1", "en2", "en3", "en4", "en5", "en6", "en7",
			"en8", "en9", "ppp0", "ppp1", "ppp2"}
	}

	if b.config.VMName == "" {
		b.config.VMName = fmt.Sprintf("packer-%s", b.config.PackerBuildName)
	}

	// Errors
	templates := map[string]*string{
		"guest_os_type":        &b.config.GuestOSType,
		"hard_drive_interface": &b.config.HardDriveInterface,
		"http_directory":       &b.config.HTTPDir,
		"iso_checksum":         &b.config.ISOChecksum,
		"iso_checksum_type":    &b.config.ISOChecksumType,
		"iso_url":              &b.config.RawSingleISOUrl,
		"vm_name":              &b.config.VMName,
	}

	for n, ptr := range templates {
		var err error
		*ptr, err = b.config.tpl.Process(*ptr, nil)
		if err != nil {
			errs = packer.MultiErrorAppend(
				errs, fmt.Errorf("Error processing %s: %s", n, err))
		}
	}

	for i, url := range b.config.ISOUrls {
		var err error
		b.config.ISOUrls[i], err = b.config.tpl.Process(url, nil)
		if err != nil {
			errs = packer.MultiErrorAppend(
				errs, fmt.Errorf("Error processing iso_urls[%d]: %s", i, err))
		}
	}

	for i, command := range b.config.BootCommand {
		if err := b.config.tpl.Validate(command); err != nil {
			errs = packer.MultiErrorAppend(errs,
				fmt.Errorf("Error processing boot_command[%d]: %s", i, err))
		}
	}

	if b.config.HardDriveInterface != "ide" && b.config.HardDriveInterface != "sata" && b.config.HardDriveInterface != "scsi" {
		errs = packer.MultiErrorAppend(
			errs, errors.New("hard_drive_interface can only be ide, sata, or scsi"))
	}

	if b.config.HTTPPortMin > b.config.HTTPPortMax {
		errs = packer.MultiErrorAppend(
			errs, errors.New("http_port_min must be less than http_port_max"))
	}

	if b.config.ISOChecksumType == "" {
		errs = packer.MultiErrorAppend(
			errs, errors.New("The iso_checksum_type must be specified."))
	} else {
		b.config.ISOChecksumType = strings.ToLower(b.config.ISOChecksumType)
		if b.config.ISOChecksumType != "none" {
			if b.config.ISOChecksum == "" {
				errs = packer.MultiErrorAppend(
					errs, errors.New("Due to large file sizes, an iso_checksum is required"))
			} else {
				b.config.ISOChecksum = strings.ToLower(b.config.ISOChecksum)
			}

			if h := common.HashForType(b.config.ISOChecksumType); h == nil {
				errs = packer.MultiErrorAppend(
					errs,
					fmt.Errorf("Unsupported checksum type: %s", b.config.ISOChecksumType))
			}
		}
	}

	if b.config.RawSingleISOUrl == "" && len(b.config.ISOUrls) == 0 {
		errs = packer.MultiErrorAppend(
			errs, errors.New("One of iso_url or iso_urls must be specified."))
	} else if b.config.RawSingleISOUrl != "" && len(b.config.ISOUrls) > 0 {
		errs = packer.MultiErrorAppend(
			errs, errors.New("Only one of iso_url or iso_urls may be specified."))
	} else if b.config.RawSingleISOUrl != "" {
		b.config.ISOUrls = []string{b.config.RawSingleISOUrl}
	}

	for i, url := range b.config.ISOUrls {
		b.config.ISOUrls[i], err = common.DownloadableURL(url)
		if err != nil {
			errs = packer.MultiErrorAppend(
				errs, fmt.Errorf("Failed to parse iso_url %d: %s", i+1, err))
		}
	}

	// Warnings
	if b.config.ISOChecksumType == "none" {
		warnings = append(warnings,
			"A checksum type of 'none' was specified. Since ISO files are so big,\n"+
				"a checksum is highly recommended.")
	}

	if b.config.ShutdownCommand == "" {
		warnings = append(warnings,
			"A shutdown_command was not specified. Without a shutdown command, Packer\n"+
				"will forcibly halt the virtual machine, which may result in data loss.")
	}

	if b.config.ParallelsToolsHostPath != "" {
		warnings = append(warnings,
			"A 'parallels_tools_host_path' has been deprecated and not in use anymore\n"+
				"You can remove it from your Packer template.")
	}

	if errs != nil && len(errs.Errors) > 0 {
		return warnings, errs
	}

	return warnings, nil
}

func (b *Builder) Run(ui packer.Ui, hook packer.Hook, cache packer.Cache) (packer.Artifact, error) {
	// Create the driver that we'll use to communicate with Parallels
	driver, err := parallelscommon.NewDriver()
	if err != nil {
		return nil, fmt.Errorf("Failed creating Parallels driver: %s", err)
	}

	steps := []multistep.Step{
		&parallelscommon.StepPrepareParallelsTools{
			ParallelsToolsFlavor: b.config.ParallelsToolsFlavor,
			ParallelsToolsMode:   b.config.ParallelsToolsMode,
		},
		&common.StepDownload{
			Checksum:     b.config.ISOChecksum,
			ChecksumType: b.config.ISOChecksumType,
			Description:  "ISO",
			ResultKey:    "iso_path",
			Url:          b.config.ISOUrls,
		},
		&parallelscommon.StepOutputDir{
			Force: b.config.PackerForce,
			Path:  b.config.OutputDir,
		},
		&common.StepCreateFloppy{
			Files: b.config.FloppyFiles,
		},
		new(stepHTTPServer),
		new(stepCreateVM),
		new(stepCreateDisk),
		new(stepSetBootOrder),
		new(stepAttachISO),
		&parallelscommon.StepAttachParallelsTools{
			ParallelsToolsMode: b.config.ParallelsToolsMode,
		},
		new(parallelscommon.StepAttachFloppy),
		&parallelscommon.StepPrlctl{
			Commands: b.config.Prlctl,
			Tpl:      b.config.tpl,
		},
		&parallelscommon.StepRun{
			BootWait: b.config.BootWait,
			Headless: b.config.Headless, // TODO: migth work on Enterprise Ed.
		},
		&parallelscommon.StepTypeBootCommand{
			BootCommand:    b.config.BootCommand,
			HostInterfaces: b.config.HostInterfaces,
			VMName:         b.config.VMName,
			Tpl:            b.config.tpl,
		},
		winparallelscommon.NewConnectStep(b.config.WinRMConfig),
		&parallelscommon.StepUploadVersion{
			Path: b.config.PrlctlVersionFile,
		},
		&parallelscommon.StepUploadParallelsTools{
			ParallelsToolsFlavor:    b.config.ParallelsToolsFlavor,
			ParallelsToolsGuestPath: b.config.ParallelsToolsGuestPath,
			ParallelsToolsMode:      b.config.ParallelsToolsMode,
			Tpl:                     b.config.tpl,
		},
		new(common.StepProvision),
		&parallelscommon.StepShutdown{
			Command: b.config.ShutdownCommand,
			Timeout: b.config.ShutdownTimeout,
		},
	}

	// Setup the state bag
	state := new(multistep.BasicStateBag)
	state.Put("cache", cache)
	state.Put("config", &b.config)
	state.Put("driver", driver)
	state.Put("hook", hook)
	state.Put("ui", ui)

	// Run
	if b.config.PackerDebug {
		b.runner = &multistep.DebugRunner{
			Steps:   steps,
			PauseFn: common.MultistepDebugFn(ui),
		}
	} else {
		b.runner = &multistep.BasicRunner{Steps: steps}
	}

	b.runner.Run(state)

	// If there was an error, return that
	if rawErr, ok := state.GetOk("error"); ok {
		return nil, rawErr.(error)
	}

	// If we were interrupted or cancelled, then just exit.
	if _, ok := state.GetOk(multistep.StateCancelled); ok {
		return nil, errors.New("Build was cancelled.")
	}

	if _, ok := state.GetOk(multistep.StateHalted); ok {
		return nil, errors.New("Build was halted.")
	}

	return parallelscommon.NewArtifact(b.config.OutputDir)
}

func (b *Builder) Cancel() {
	if b.runner != nil {
		log.Println("Cancelling the step runner...")
		b.runner.Cancel()
	}
}
