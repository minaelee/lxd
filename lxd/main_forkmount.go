package main

/*
#include "config.h"

#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <libgen.h>
#include <limits.h>
#include <lxc/lxccontainer.h>
#include <lxc/version.h>
#include <sched.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

#include "lxd.h"
#include "memory_utils.h"
#include "mount_utils.h"
#include "process_utils.h"
#include "syscall_numbers.h"
#include "syscall_wrappers.h"
#include "../shared/netutils/unixfd.h"

#define VERSION_AT_LEAST(major, minor, micro)							\
	((LXC_DEVEL == 1) || (!(major > LXC_VERSION_MAJOR ||					\
	major == LXC_VERSION_MAJOR && minor > LXC_VERSION_MINOR ||				\
	major == LXC_VERSION_MAJOR && minor == LXC_VERSION_MINOR && micro > LXC_VERSION_MICRO)))

static int mkdir_p(const char *dir, mode_t mode)
{
	const char *tmp = dir;
	const char *orig = dir;

	do {
		__do_free char *makeme = NULL;

		dir = tmp + strspn(tmp, "/");
		tmp = dir + strcspn(dir, "/");
		makeme = strndup(orig, dir - orig);
		if (*makeme) {
			if (mkdir(makeme, mode) && errno != EEXIST) {
				fprintf(stderr, "failed to create directory '%s': %s\n", makeme, strerror(errno));
				return -1;
			}
		}
	} while(tmp != dir);

	return 0;
}

static void ensure_dir(char *dest) {
	struct stat sb;
	if (stat(dest, &sb) == 0) {
		if ((sb.st_mode & S_IFMT) == S_IFDIR)
			return;
		if (unlink(dest) < 0) {
			fprintf(stderr, "Failed to remove old %s: %s\n", dest, strerror(errno));
			_exit(1);
		}
	}
	if (mkdir(dest, 0755) < 0) {
		fprintf(stderr, "Failed to mkdir %s: %s\n", dest, strerror(errno));
		_exit(1);
	}
}

static void ensure_file(char *dest)
{
	__do_close int fd = -EBADF;
	struct stat sb;

	if (stat(dest, &sb) == 0) {
		if ((sb.st_mode & S_IFMT) != S_IFDIR)
			return;
		if (rmdir(dest) < 0) {
			fprintf(stderr, "Failed to remove old %s: %s\n", dest, strerror(errno));
			_exit(1);
		}
	}

	fd = creat(dest, 0755);
	if (fd < 0) {
		fprintf(stderr, "Failed to mkdir %s: %s\n", dest, strerror(errno));
		_exit(1);
	}
}

static void create(int fd_src, char *src, char *dest)
{
	__do_free char *dirdup = NULL;
	char *destdirname;
	struct stat sb;

	if (src) {
		if (stat(src, &sb) < 0)
			die("source %s does not exist", src);
	} else {
		if (fstat(fd_src, &sb) < 0)
			die("source %s does not exist", src);
	}

	dirdup = strdup(dest);
	if (!dirdup)
		_exit(1);

	destdirname = dirname(dirdup);

	if (mkdir_p(destdirname, 0755) < 0) {
		fprintf(stderr, "failed to create path: %s\n", destdirname);
		_exit(1);
	}

	switch (sb.st_mode & S_IFMT) {
	case S_IFDIR:
		ensure_dir(dest);
		return;
	default:
		ensure_file(dest);
		return;
	}
}

static int lxc_safe_ulong(const char *numstr, unsigned long *converted)
{
	char *err = NULL;
	unsigned long int uli;

	while (isspace(*numstr))
		numstr++;

	if (*numstr == '-')
		return -EINVAL;

	errno = 0;
	uli = strtoul(numstr, &err, 0);
	if (errno == ERANGE && uli == ULONG_MAX)
		return -ERANGE;

	if (err == numstr || *err != '\0')
		return -EINVAL;

	*converted = uli;
	return 0;
}

static void do_lxd_forkmount(int pidfd, int ns_fd)
{
	unsigned long mntflags = 0;
	int fd_tree = -EBADF;
	int ret;
	char *src, *dest, *idmapType, *flags;

	src = advance_arg(true);
	dest = advance_arg(true);
	idmapType = advance_arg(true);
	flags = advance_arg(true);

	if (strcmp(idmapType, "idmapped") == 0) {
		int fd_mntns, fd_userns;

		fd_userns = preserve_ns(-ESRCH, ns_fd, "user");
		if (fd_userns < 0) {
			fprintf(stderr, "Failed to open user namespace of container: %s\n", strerror(errno));
			_exit(1);
		}

		fd_mntns = preserve_ns(getpid(), -EBADF, "mnt");
		if (fd_mntns < 0) {
			fprintf(stderr, "Failed to open mount namespace of container: %s\n", strerror(errno));
			_exit(1);
		}

		if (!change_namespaces(pidfd, ns_fd, CLONE_NEWNS)) {
			fprintf(stderr, "Failed setns to container mount namespace: %s\n", strerror(errno));
			_exit(1);
		}

		fd_tree = mount_detach_idmap(src, fd_userns);
		if (fd_tree < 0) {
			fprintf(stderr, "Failed to create detached idmapped mount \"%s\": %s\n", src, strerror(errno));
			_exit(1);
		}

		ret = setns(fd_mntns, CLONE_NEWNS);
		if (ret) {
			fprintf(stderr, "Failed to switch to original mount namespace: %s\n", strerror(errno));
			_exit(1);
		}

		close_prot_errno_disarm(fd_userns);
		close_prot_errno_disarm(fd_mntns);
	}

	attach_userns_fd(ns_fd);

	if (!change_namespaces(pidfd, ns_fd, CLONE_NEWNS)) {
		fprintf(stderr, "Failed setns to container mount namespace: %s\n", strerror(errno));
		_exit(1);
	}

	create(-EBADF, src, dest);

	if (access(src, F_OK) < 0) {
		fprintf(stderr, "Mount source doesn't exist: %s\n", strerror(errno));
		_exit(1);
	}

	if (access(dest, F_OK) < 0) {
		fprintf(stderr, "Mount destination doesn't exist: %s\n", strerror(errno));
		_exit(1);
	}

	if (fd_tree >= 0) {
		ret = lxd_move_mount(fd_tree, "", -EBADF, dest, MOVE_MOUNT_F_EMPTY_PATH);
		if (ret) {
			fprintf(stderr, "Failed to move detached mount to target from %d to %s: %s\n", fd_tree, dest, strerror(errno));
			_exit(1);
		}

		close_prot_errno_disarm(fd_tree);
		_exit(0);
	}

	ret = lxc_safe_ulong(flags, &mntflags);
	if (ret < 0)
		_exit(1);

	// Here, we always move recursively, because we sometimes allow
	// recursive mounts. If the mount has no kids then it doesn't matter,
	// but if it does, we want to move those too.
	if (mount(src, dest, "none", MS_MOVE | MS_REC, NULL) < 0) {
		fprintf(stderr, "Failed mounting %s onto %s: %s\n", src, dest, strerror(errno));
		_exit(1);
	}

	_exit(0);
}

static int lxc_safe_uint(const char *numstr, unsigned int *converted)
{
	char *err = NULL;
	unsigned long int uli;

	while (isspace(*numstr))
		numstr++;

	if (*numstr == '-')
		return -EINVAL;

	errno = 0;
	uli = strtoul(numstr, &err, 0);
	if (errno == ERANGE && uli == ULONG_MAX)
		return -ERANGE;

	if (err == numstr || *err != '\0')
		return -EINVAL;

	if (uli > UINT_MAX)
		return -ERANGE;

	*converted = (unsigned int)uli;
	return 0;
}

static int mnt_attributes_new(unsigned int old_flags, unsigned int *new_flags)
{
	unsigned int flags = 0;

	if (old_flags & MS_RDONLY) {
		flags |= MOUNT_ATTR_RDONLY;
		old_flags &= ~MS_RDONLY;
	}

	if (old_flags & MS_NOSUID) {
		flags |= MOUNT_ATTR_NOSUID;
		old_flags &= ~MS_NOSUID;
	}

	if (old_flags & MS_NODEV) {
		flags |= MOUNT_ATTR_NODEV;
		old_flags &= ~MS_NODEV;
	}

	if (old_flags & MS_NOEXEC) {
		flags |= MOUNT_ATTR_NOEXEC;
		old_flags &= ~MS_NOEXEC;
	}

	if (old_flags & MS_RELATIME) {
		flags |= MOUNT_ATTR_RELATIME;
		old_flags &= ~MS_RELATIME;
	}

	if (old_flags & MS_NOATIME) {
		flags |= MOUNT_ATTR_NOATIME;
		old_flags &= ~MS_NOATIME;
	}

	if (old_flags & MS_STRICTATIME) {
		flags |= MOUNT_ATTR_STRICTATIME;
		old_flags &= ~MS_STRICTATIME;
	}

	if (old_flags & MS_NODIRATIME) {
		flags |= MOUNT_ATTR_NODIRATIME;
		old_flags &= ~MS_NODIRATIME;
	}

	*new_flags |= flags;
	return old_flags;
}

static int make_final_open(struct stat *st_src, const char *dest)
{
	int ret;
	int flags = O_CLOEXEC | O_NOFOLLOW | O_NOCTTY | O_CLOEXEC;
	struct stat st_dest;

	ret = stat(dest, &st_dest);
	if (ret == 0) {
		if ((st_dest.st_mode & S_IFMT) == (st_src->st_mode & S_IFMT))
			goto out_open;

		ret = remove(dest);
		if (ret)
			return -1;
	}

	if ((st_src->st_mode & S_IFMT) == S_IFDIR)
		ret = mkdir(dest, 0000);
	else
		ret = mknod(dest, S_IFREG | 0000, 0);
	if (ret)
		return -1;

out_open:
	return open(dest, flags | O_PATH);
}

static int make_dest_open(int fd_src, const char *dest)
{
	__do_free char *dirdup = NULL;
	int ret;
	char *destdirname;
	struct stat st_src;

	ret = fstat(fd_src, &st_src);
	if (ret)
		return -1;

	dirdup = strdup(dest);
	if (!dirdup)
		return -1;

	destdirname = dirname(dirdup);

	ret = mkdir_p(destdirname, 0755);
	if (ret)
		return -1;

	return make_final_open(&st_src, dest);
}

static void do_move_forkmount(int pidfd, int ns_fd)
{
	__do_close int fs_fd = -EBADF, mnt_fd = -EBADF, fd_userns = -EBADF,
		       dest_fd = -EBADF;
	int ret;
	char *fstype, *src, *dest, *idmapType, *flags;
	unsigned int old_mntflags = 0, new_mntflags = 0;

	fstype = advance_arg(true);
	src = advance_arg(true);
	dest = advance_arg(true);
	idmapType = advance_arg(true);
	flags = advance_arg(true);

	ret = lxc_safe_uint(flags, &old_mntflags);
	if (ret < 0)
		die("parse mount flags");

	mnt_attributes_new(old_mntflags, &new_mntflags);

	if (strcmp(fstype, "") && strcmp(fstype, "none")) {
		fs_fd = lxd_fsopen(fstype, FSOPEN_CLOEXEC);
		if (fs_fd < 0)
			die("fsopen: %s", fstype);

		ret = lxd_fsconfig(fs_fd, FSCONFIG_SET_STRING, "source", src, 0);
		if (ret < 0)
			die("fsconfig: source");

		ret = lxd_fsconfig(fs_fd, FSCONFIG_CMD_CREATE, NULL, NULL, 0);
		if (ret < 0)
			die("fsconfig: create");

		mnt_fd = lxd_fsmount(fs_fd, FSMOUNT_CLOEXEC, new_mntflags);
		if (mnt_fd < 0)
			die("fsmount");
	} else {
		mnt_fd = lxd_open_tree(-EBADF, src, OPEN_TREE_CLOEXEC | OPEN_TREE_CLONE);
		if (mnt_fd < 0)
			die("open_tree");
	}

	fd_userns = preserve_ns(-ESRCH, ns_fd, "user");
	if (fd_userns < 0)
		die("preserve userns");

	if (strcmp(idmapType, "idmapped") == 0) {
		struct lxc_mount_attr attr = {
			.attr_set	= MOUNT_ATTR_IDMAP,
			.userns_fd	= fd_userns,

		};

		ret = lxd_mount_setattr(mnt_fd, "", AT_EMPTY_PATH, &attr, sizeof(attr));
		if (ret)
			die("idmap mount");
	}

	attach_userns_fd(ns_fd);

	if (!change_namespaces(pidfd, ns_fd, CLONE_NEWNS))
		die("Failed setns to container mount namespace");

	dest_fd = make_dest_open(mnt_fd, dest);
	if (dest_fd < 0)
		die("Failed to create destination mount point");

	ret = lxd_move_mount(mnt_fd, "", dest_fd, "",
			     MOVE_MOUNT_F_EMPTY_PATH | MOVE_MOUNT_T_EMPTY_PATH);
	if (ret)
		die("Failed to move detached mount to target from %d to %s", mnt_fd, dest);

	_exit(EXIT_SUCCESS);
}

static void do_lxd_forkumount(int pidfd, int ns_fd)
{
	int ret;
	char *path = NULL;

	if (!change_namespaces(pidfd, ns_fd, CLONE_NEWNS)) {
		fprintf(stderr, "Failed to setns to container mount namespace: %s\n", strerror(errno));
		_exit(1);
	}

	path = advance_arg(true);

	ret = umount2(path, MNT_DETACH);
	if (ret < 0) {
		// - ENOENT: The user must have unmounted and removed the path.
		// - EINVAL: The user must have unmounted. Other explanations
		//           for EINVAL do not apply.
		if (errno == ENOENT || errno == EINVAL)
			_exit(0);

		fprintf(stderr, "Error unmounting %s: %s\n", path, strerror(errno));
		_exit(1);
	}

	_exit(0);
}

static void do_lxc_forkmount(void)
{
#if VERSION_AT_LEAST(3, 1, 0)
	int ret;
	char *config, *flags, *fstype, *lxcpath, *name, *source, *target;
	struct lxc_container *c;
	struct lxc_mount mnt = {0};
	unsigned long mntflags = 0;

	name = advance_arg(true);
	lxcpath = advance_arg(true);
	config = advance_arg(true);
	source = advance_arg(true);
	target = advance_arg(true);
	fstype = advance_arg(true);
	flags = advance_arg(true);

	c = lxc_container_new(name, lxcpath);
	if (!c)
		_exit(1);

	c->clear_config(c);

	if (!c->load_config(c, config)) {
		lxc_container_put(c);
		_exit(1);
	}

	ret = lxc_safe_ulong(flags, &mntflags);
	if (ret < 0) {
		lxc_container_put(c);
		_exit(1);
	}

	ret = c->mount(c, source, target, fstype, mntflags, NULL, &mnt);
	lxc_container_put(c);
	if (ret < 0)
		_exit(1);

	_exit(0);
#else
	fprintf(stderr, "error: Called lxc_forkmount when missing LXC support\n");
	_exit(1);
#endif
}

static void do_lxc_forkumount(void)
{
#if VERSION_AT_LEAST(3, 1, 0)
	int ret;
	char *config, *lxcpath, *name, *target;
	struct lxc_container *c;
	struct lxc_mount mnt = {0};

	name = advance_arg(true);
	lxcpath = advance_arg(true);
	config = advance_arg(true);
	target = advance_arg(true);

	c = lxc_container_new(name, lxcpath);
	if (!c)
		_exit(1);

	c->clear_config(c);

	if (!c->load_config(c, config)) {
		lxc_container_put(c);
		_exit(1);
	}

	ret = c->umount(c, target, MNT_DETACH, &mnt);
	lxc_container_put(c);
	if (ret < 0)
		_exit(1);

	_exit(0);
#else
	fprintf(stderr, "error: Called lxc_forkumount when missing LXC support\n");
	_exit(1);
#endif
}

static void do_mount_bpffs(int pidfd, int ns_fd)
{
	__do_close int fs_fd = -EBADF, mnt_fd = -EBADF;
	int ret;
	char *mountpoint;
	char *delegate_cmds, *delegate_maps, *delegate_progs, *delegate_attachs;
	int sk_fds[2] = {-EBADF, -EBADF};
	pid_t child_pid = -1;
	struct unix_fds fds = {};
	char buf[50];
	char *bpffs_fd_ready = "BPFFSFDREADY";
	char *bpffs_mnt_ready = "BPFFSMNTREADY";

	mountpoint = advance_arg(true);
	delegate_cmds = advance_arg(true);
	delegate_maps = advance_arg(true);
	delegate_progs = advance_arg(true);
	delegate_attachs = advance_arg(true);

	ret = socketpair(AF_UNIX, SOCK_DGRAM | SOCK_CLOEXEC, 0, sk_fds);
	if (ret < 0) {
		fprintf(stderr,
			"%s - Failed to create anonymous unix socket pair\n",
			strerror(errno));
		exit(EXIT_FAILURE);
	}

	child_pid = fork();
	if (child_pid < 0) {
		fprintf(stderr,
			"%s - Failed to fork()\n",
			strerror(errno));
		exit(EXIT_FAILURE);
	}

	if (child_pid == 0) {
		__do_close int mountpoint_fd = -EBADF;

		// close parent's socket
		close(sk_fds[0]);

		// 1. Go into container's user & mount namespace

		attach_userns_fd(ns_fd);

		if (!change_namespaces(pidfd, ns_fd, CLONE_NEWNS))
			die("Failed setns to container mount namespace");

		// 2. Create bpf filesystem file_context fd (tied to users)

		fs_fd = lxd_fsopen("bpf", FSOPEN_CLOEXEC);
		if (fs_fd < 0)
			die("fsopen: bpf");

		// 3. Send bpf filesystem file_context fd to a parent process

		ret = lxc_abstract_unix_send_fds(sk_fds[1], &fs_fd, 1, bpffs_fd_ready, sizeof(bpffs_fd_ready));
		if (ret < 0)
			die("lxc_abstract_unix_send_fds(bpffs_fd_ready) failed");

		close_prot_errno_disarm(fs_fd);

		// 8. Get the detached mount of bpf filesystem from the parent process

		fds.fd_count_max = 1;
		fds.flags = UNIX_FDS_ACCEPT_EXACT;
		ret = lxc_abstract_unix_recv_fds(sk_fds[1], &fds, buf, sizeof(buf));
		if (ret < 0 || fds.fd_count_ret != 1 || strncmp(buf, bpffs_mnt_ready, sizeof(bpffs_mnt_ready)))
			die("lxc_abstract_unix_recv_fds(bpffs_mnt_ready) failed");
		mnt_fd = fds.fd[0];

		mountpoint_fd = make_dest_open(mnt_fd, mountpoint);
		if (mountpoint_fd < 0)
			die("Failed to create destination mount point");

		// 9. Move the detached mount of bpf filesystem to a right place in the container's mount namespace

		ret = lxd_move_mount(mnt_fd, "", mountpoint_fd, "",
				MOVE_MOUNT_F_EMPTY_PATH | MOVE_MOUNT_T_EMPTY_PATH);
		if (ret)
			die("Failed to move detached mount to target from %d to %s", mnt_fd, mountpoint);

		exit(EXIT_SUCCESS);
	}

	// close child's socket
	close(sk_fds[1]);

	// 4. Get bpf filesystem file_context fd from a child process

	fds.fd_count_max = 1;
	fds.flags = UNIX_FDS_ACCEPT_EXACT;
	ret = lxc_abstract_unix_recv_fds(sk_fds[0], &fds, buf, sizeof(buf));
	if (ret < 0 || fds.fd_count_ret != 1 || strncmp(buf, bpffs_fd_ready, sizeof(bpffs_fd_ready))) {
		fprintf(stderr,
			"%s - lxc_abstract_unix_recv_fds(bpffs_fd_ready) failed\n",
			strerror(errno));
		goto err_process;
	}

	fs_fd = fds.fd[0];

	// 5. Configure bpf filesystem file_context fd to set BPF token delegation properties

	ret = lxd_fsconfig(fs_fd, FSCONFIG_SET_STRING, "delegate_cmds", delegate_cmds, 0);
	if (ret < 0) {
		fprintf(stderr,
			"%s - fsconfig failed to set delegate_cmds=%s\n",
			strerror(errno),
			delegate_cmds);
		goto err_process;
	}

	ret = lxd_fsconfig(fs_fd, FSCONFIG_SET_STRING, "delegate_maps", delegate_maps, 0);
	if (ret < 0) {
		fprintf(stderr,
			"%s - fsconfig failed to set delegate_maps=%s\n",
			strerror(errno),
			delegate_maps);
		goto err_process;
	}

	ret = lxd_fsconfig(fs_fd, FSCONFIG_SET_STRING, "delegate_progs", delegate_progs, 0);
	if (ret < 0) {
		fprintf(stderr,
			"%s - fsconfig failed to set delegate_progs=%s\n",
			strerror(errno),
			delegate_progs);
		goto err_process;
	}

	ret = lxd_fsconfig(fs_fd, FSCONFIG_SET_STRING, "delegate_attachs", delegate_attachs, 0);
	if (ret < 0) {
		fprintf(stderr,
			"%s - fsconfig failed to set delegate_attachs=%s\n",
			strerror(errno),
			delegate_attachs);
		goto err_process;
	}

	ret = lxd_fsconfig(fs_fd, FSCONFIG_CMD_CREATE, NULL, NULL, 0);
	if (ret < 0) {
		fprintf(stderr,
			"%s - fsconfig(FSCONFIG_CMD_CREATE) failed\n",
			strerror(errno));
		goto err_process;
	}

	// 6. Make a detached mount of bpf filesystem from a file_context fd

	mnt_fd = lxd_fsmount(fs_fd, FSMOUNT_CLOEXEC, 0);
	if (mnt_fd < 0) {
		fprintf(stderr,
			"%s - fsmount failed\n",
			strerror(errno));
		goto err_process;
	}

	// 7. Send the detached mount of bpf filesystem to the child process

	ret = lxc_abstract_unix_send_fds(sk_fds[0], &mnt_fd, 1, bpffs_mnt_ready, sizeof(bpffs_mnt_ready));
	if (ret < 0) {
		fprintf(stderr,
			"%s - lxc_abstract_unix_send_fds(bpffs_mnt_ready) failed\n",
			strerror(errno));
		goto err_process;
	}

	// 10. End

	ret = wait_for_pid(child_pid);
	if (ret)
		die("wait_for_pid");

	exit(EXIT_SUCCESS);

err_process:
	if (child_pid > 0) {
		kill(child_pid, SIGKILL);
		wait_for_pid(child_pid);
	}

	exit(EXIT_FAILURE);
}

void forkmount(void)
{
	char *command = NULL, *cur = NULL;
	int ns_fd = -EBADF, pidfd = -EBADF;
	pid_t pid = 0;

	// Get the subcommand
	command = advance_arg(false);
	if (command == NULL || (strcmp(command, "--help") == 0 || strcmp(command, "--version") == 0 || strcmp(command, "-h") == 0))
		return;

	// Check that we're root
	if (geteuid() != 0) {
		fprintf(stderr, "Error: forkmount requires root privileges\n");
		_exit(1);
	}

	// skip "--"
	advance_arg(true);

	// Call the subcommands
	if (strcmp(command, "lxd-mount") == 0) {
		// Get the pid
		cur = advance_arg(false);
		if (cur == NULL || (strcmp(cur, "--help") == 0 || strcmp(cur, "--version") == 0 || strcmp(cur, "-h") == 0))
			return;

		pid = atoi(cur);
		if (pid <= 0)
			_exit(EXIT_FAILURE);

		pidfd = atoi(advance_arg(true));
		ns_fd = pidfd_nsfd(pidfd, pid);
		if (ns_fd < 0)
			_exit(EXIT_FAILURE);

		do_lxd_forkmount(pidfd, ns_fd);
	} else if (strcmp(command, "lxc-mount") == 0) {
		do_lxc_forkmount();
	} else if (strcmp(command, "move-mount") == 0) {
		// Get the pid
		cur = advance_arg(false);
		if (cur == NULL || (strcmp(cur, "--help") == 0 || strcmp(cur, "--version") == 0 || strcmp(cur, "-h") == 0))
			return;

		pid = atoi(cur);
		if (pid <= 0)
			_exit(EXIT_FAILURE);

		pidfd = atoi(advance_arg(true));
		ns_fd = pidfd_nsfd(pidfd, pid);
		if (ns_fd < 0)
			_exit(EXIT_FAILURE);

		do_move_forkmount(pidfd, ns_fd);
	} else if (strcmp(command, "lxd-umount") == 0) {
		// Get the pid
		cur = advance_arg(false);
		if (cur == NULL || (strcmp(cur, "--help") == 0 || strcmp(cur, "--version") == 0 || strcmp(cur, "-h") == 0))
			return;

		pid = atoi(cur);
		if (pid <= 0)
			_exit(EXIT_FAILURE);

		pidfd = atoi(advance_arg(true));
		ns_fd = pidfd_nsfd(pidfd, pid);
		if (ns_fd < 0)
			_exit(EXIT_FAILURE);

		do_lxd_forkumount(pidfd, ns_fd);
	} else if (strcmp(command, "lxc-umount") == 0) {
		do_lxc_forkumount();
	} else if (strcmp(command, "bpffs") == 0) {
		// Get the pid
		cur = advance_arg(false);
		if (cur == NULL || (strcmp(cur, "--help") == 0 || strcmp(cur, "--version") == 0 || strcmp(cur, "-h") == 0))
			return;

		pid = atoi(cur);
		if (pid <= 0)
			_exit(EXIT_FAILURE);

		pidfd = atoi(advance_arg(true));
		ns_fd = pidfd_nsfd(pidfd, pid);
		if (ns_fd < 0)
			_exit(EXIT_FAILURE);

		do_mount_bpffs(pidfd, ns_fd);
	}
}
*/
import "C"

import (
	"errors"

	"github.com/spf13/cobra"

	_ "github.com/canonical/lxd/lxd/include" // Used by cgo
)

type cmdForkmount struct {
	global *cmdGlobal
}

// Command returns a cobra.Command for use with (*cobra.Command).AddCommand.
func (c *cmdForkmount) Command() *cobra.Command {
	// Main subcommand
	cmd := &cobra.Command{}
	cmd.Use = "forkmount"
	cmd.Short = "Perform mount operations"
	cmd.Long = `Description:
  Perform mount operations

  This set of internal commands are used for all container mount
  operations.
`
	cmd.Hidden = true

	// mount
	cmdLXCMount := &cobra.Command{}
	cmdLXCMount.Use = "lxc-mount <name> <lxcpath> <configpath> <source> <destination> <fstype> <flags>"
	cmdLXCMount.Args = cobra.ExactArgs(7)
	cmdLXCMount.RunE = c.Run
	cmd.AddCommand(cmdLXCMount)

	cmdLXDMount := &cobra.Command{}
	cmdLXDMount.Use = "lxd-mount <PID> <PidFd> <source> <destination> <idmapType> <flags>"
	cmdLXDMount.Args = cobra.ExactArgs(6)
	cmdLXDMount.RunE = c.Run
	cmd.AddCommand(cmdLXDMount)

	// umount
	cmdLXCUmount := &cobra.Command{}
	cmdLXCUmount.Use = "lxc-umount <name> <lxcpath> <configpath> <path>"
	cmdLXCUmount.Args = cobra.ExactArgs(4)
	cmdLXCUmount.RunE = c.Run
	cmd.AddCommand(cmdLXCUmount)

	cmdLXDUmount := &cobra.Command{}
	cmdLXDUmount.Use = "lxd-umount <PID> <PidFd> <path>"
	cmdLXDUmount.Args = cobra.ExactArgs(3)
	cmdLXDUmount.RunE = c.Run
	cmd.AddCommand(cmdLXDUmount)

	// Workaround for subcommand usage errors. See: https://github.com/spf13/cobra/issues/706
	cmd.Args = cobra.NoArgs
	cmd.Run = func(cmd *cobra.Command, args []string) { _ = cmd.Usage() }
	return cmd
}

// Run executes the forkmount command.
func (c *cmdForkmount) Run(cmd *cobra.Command, args []string) error {
	return errors.New("This command should have been intercepted in cgo")
}
