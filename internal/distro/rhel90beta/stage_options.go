package rhel90beta

import (
	"fmt"
	"path/filepath"

	"github.com/osbuild/osbuild-composer/internal/blueprint"
	osbuild "github.com/osbuild/osbuild-composer/internal/osbuild2"
)

const (
	kspath = "/osbuild.ks"
)

// selinuxStageOptions returns the options for the org.osbuild.selinux stage.
// Setting the argument to 'true' relabels the '/usr/bin/cp' and '/usr/bin/tar'
// binaries with 'install_exec_t'. This should be set in the build root.
func selinuxStageOptions(labelcp bool) *osbuild.SELinuxStageOptions {
	options := &osbuild.SELinuxStageOptions{
		FileContexts: "etc/selinux/targeted/contexts/files/file_contexts",
	}
	if labelcp {
		options.Labels = map[string]string{
			"/usr/bin/cp":  "system_u:object_r:install_exec_t:s0",
			"/usr/bin/tar": "system_u:object_r:install_exec_t:s0",
		}
	}
	return options
}

func usersFirstBootOptions(usersStageOptions *osbuild.UsersStageOptions) *osbuild.FirstBootStageOptions {
	cmds := make([]string, 0, 3*len(usersStageOptions.Users)+1)
	// workaround for creating authorized_keys file for user
	varhome := filepath.Join("/var", "home")
	for name, user := range usersStageOptions.Users {
		if user.Key != nil {
			sshdir := filepath.Join(varhome, name, ".ssh")
			cmds = append(cmds, fmt.Sprintf("mkdir -p %s", sshdir))
			cmds = append(cmds, fmt.Sprintf("sh -c 'echo %q >> %q'", *user.Key, filepath.Join(sshdir, "authorized_keys")))
			cmds = append(cmds, fmt.Sprintf("chown %s:%s -Rc %s", name, name, sshdir))
		}
	}
	cmds = append(cmds, fmt.Sprintf("restorecon -rvF %s", varhome))
	options := &osbuild.FirstBootStageOptions{
		Commands:       cmds,
		WaitForNetwork: false,
	}

	return options
}

func firewallStageOptions(firewall *blueprint.FirewallCustomization) *osbuild.FirewallStageOptions {
	options := osbuild.FirewallStageOptions{
		Ports: firewall.Ports,
	}

	if firewall.Services != nil {
		options.EnabledServices = firewall.Services.Enabled
		options.DisabledServices = firewall.Services.Disabled
	}

	return &options
}

func systemdStageOptions(enabledServices, disabledServices []string, s *blueprint.ServicesCustomization, target string) *osbuild.SystemdStageOptions {
	if s != nil {
		enabledServices = append(enabledServices, s.Enabled...)
		disabledServices = append(disabledServices, s.Disabled...)
	}
	return &osbuild.SystemdStageOptions{
		EnabledServices:  enabledServices,
		DisabledServices: disabledServices,
		DefaultTarget:    target,
	}
}

func buildStampStageOptions(arch string) *osbuild.BuildstampStageOptions {
	return &osbuild.BuildstampStageOptions{
		Arch:    arch,
		Product: "Red Hat Enterprise Linux",
		Version: osVersion,
		Variant: "edge",
		Final:   true,
	}
}

func loraxScriptStageOptions(arch string) *osbuild.LoraxScriptStageOptions {
	return &osbuild.LoraxScriptStageOptions{
		Path:     "99-generic/runtime-postinstall.tmpl",
		BaseArch: arch,
	}
}

func dracutStageOptions(kernelVer string) *osbuild.DracutStageOptions {
	kernel := []string{kernelVer}
	modules := []string{
		"bash",
		"systemd",
		"fips",
		"systemd-initrd",
		"modsign",
		"nss-softokn",
		"rdma",
		"rngd",
		"i18n",
		"convertfs",
		"network-manager",
		"network",
		"ifcfg",
		"url-lib",
		"drm",
		"plymouth",
		"prefixdevname",
		"prefixdevname-tools",
		"anaconda",
		"crypt",
		"dm",
		"dmsquash-live",
		"kernel-modules",
		"kernel-modules-extra",
		"kernel-network-modules",
		"livenet",
		"lvm",
		"mdraid",
		"multipath",
		"qemu",
		"qemu-net",
		"fcoe",
		"fcoe-uefi",
		"iscsi",
		"lunmask",
		"nfs",
		"resume",
		"rootfs-block",
		"terminfo",
		"udev-rules",
		"biosdevname",
		"dracut-systemd",
		"pollcdrom",
		"usrmount",
		"base",
		"fs-lib",
		"img-lib",
		"shutdown",
		"uefi-lib",
	}
	return &osbuild.DracutStageOptions{
		Kernel:  kernel,
		Modules: modules,
		Install: []string{"/.buildstamp"},
	}
}

func bootISOMonoStageOptions(kernelVer string, arch string) *osbuild.BootISOMonoStageOptions {
	comprOptions := new(osbuild.FSCompressionOptions)
	if bcj := osbuild.BCJOption(arch); bcj != "" {
		comprOptions.BCJ = bcj
	}
	isolabel := fmt.Sprintf("RHEL-9-0-0-BaseOS-%s", arch)
	return &osbuild.BootISOMonoStageOptions{
		Product: osbuild.Product{
			Name:    "Red Hat Enterprise Linux",
			Version: osVersion,
		},
		ISOLabel:   isolabel,
		Kernel:     kernelVer,
		KernelOpts: fmt.Sprintf("inst.ks=hd:LABEL=%s:%s", isolabel, kspath),
		EFI: osbuild.EFI{
			Architectures: []string{
				"IA32",
				"X64",
			},
			Vendor: "redhat",
		},
		ISOLinux: osbuild.ISOLinux{
			Enabled: true,
			Debug:   false,
		},
		Templates: "80-rhel",
		RootFS: osbuild.RootFS{
			Size: 9216,
			Compression: osbuild.FSCompression{
				Method:  "xz",
				Options: comprOptions,
			},
		},
	}
}

func discinfoStageOptions(arch string) *osbuild.DiscinfoStageOptions {
	return &osbuild.DiscinfoStageOptions{
		BaseArch: arch,
		Release:  "202010217.n.0",
	}
}

func xorrisofsStageOptions(filename string, arch string) *osbuild.XorrisofsStageOptions {
	return &osbuild.XorrisofsStageOptions{
		Filename: filename,
		VolID:    fmt.Sprintf("RHEL-9-0-0-BaseOS-%s", arch),
		SysID:    "LINUX",
		Boot: &osbuild.XorrisofsBoot{
			Image:   "isolinux/isolinux.bin",
			Catalog: "isolinux/boot.cat",
		},
		EFI:          "images/efiboot.img",
		IsohybridMBR: "/usr/share/syslinux/isohdpfx.bin",
	}
}
