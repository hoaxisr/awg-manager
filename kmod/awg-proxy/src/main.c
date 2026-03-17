// SPDX-License-Identifier: GPL-2.0
/*
 * AWG Proxy - Kernel UDP proxy for WG<->AWG packet transformation
 *
 * Creates per-tunnel UDP proxy instances that relay packets between
 * the local WireGuard interface and the remote AmneziaWG server,
 * transforming packets in both directions.
 *
 * Configuration via /proc/awg_proxy/{add,del,list,version}
 */

#include <linux/module.h>
#include <linux/kernel.h>
#include <linux/init.h>
#include <linux/slab.h>
#include <linux/proc_fs.h>
#include <linux/uaccess.h>
#include <linux/inet.h>
#include <linux/version.h>

#include "proxy.h"

#ifndef AWG_PROXY_VERSION
#define AWG_PROXY_VERSION "dev"
#endif

MODULE_LICENSE("GPL");
MODULE_AUTHOR("hoaxisr");
MODULE_DESCRIPTION("AWG Proxy - Kernel UDP proxy for WG<->AWG transformation");
MODULE_VERSION(AWG_PROXY_VERSION);

/* ────────────────────────── Procfs ───────────────────────────────── */

static struct proc_dir_entry *proc_dir;

/*
 * /proc/awg_proxy/add - write tunnel config to create a proxy
 * Format: "IP:PORT H1=min-max H2=... S1=N ... PUB_SERVER=hex PUB_CLIENT=hex I1=\"...\" ..."
 */
static ssize_t proc_add_write(struct file *file, const char __user *buf,
			      size_t count, loff_t *ppos)
{
	char *kbuf;
	int ret;

	if (count > 4096)
		return -EINVAL;

	kbuf = kmalloc(count + 1, GFP_KERNEL);
	if (!kbuf)
		return -ENOMEM;

	if (copy_from_user(kbuf, buf, count)) {
		kfree(kbuf);
		return -EFAULT;
	}
	kbuf[count] = '\0';

	/* Strip trailing newline */
	if (count > 0 && kbuf[count - 1] == '\n')
		kbuf[count - 1] = '\0';

	ret = awg_proxy_add(kbuf);
	kfree(kbuf);

	if (ret)
		return ret;
	return count;
}

#if LINUX_VERSION_CODE >= KERNEL_VERSION(5, 6, 0)
static const struct proc_ops proc_add_ops = {
	.proc_write = proc_add_write,
};
#else
static const struct file_operations proc_add_ops = {
	.owner = THIS_MODULE,
	.write = proc_add_write,
};
#endif

/*
 * /proc/awg_proxy/del - write "IP:PORT" to remove a proxy
 */
static ssize_t proc_del_write(struct file *file, const char __user *buf,
			      size_t count, loff_t *ppos)
{
	char kbuf[64];
	char *colon;
	__be32 ip;
	__be16 port;
	int port_int, ret;

	if (count >= sizeof(kbuf))
		return -EINVAL;

	if (copy_from_user(kbuf, buf, count))
		return -EFAULT;
	kbuf[count] = '\0';

	if (count > 0 && kbuf[count - 1] == '\n')
		kbuf[count - 1] = '\0';

	colon = strrchr(kbuf, ':');
	if (!colon)
		return -EINVAL;
	*colon = '\0';

	ip = in_aton(kbuf);
	if (kstrtoint(colon + 1, 10, &port_int) || port_int <= 0 || port_int > 65535)
		return -EINVAL;
	port = htons(port_int);

	ret = awg_proxy_del(ip, port);
	if (ret)
		return ret;
	return count;
}

#if LINUX_VERSION_CODE >= KERNEL_VERSION(5, 6, 0)
static const struct proc_ops proc_del_ops = {
	.proc_write = proc_del_write,
};
#else
static const struct file_operations proc_del_ops = {
	.owner = THIS_MODULE,
	.write = proc_del_write,
};
#endif

/*
 * /proc/awg_proxy/list - read active proxy list (includes listen_port)
 */
static ssize_t proc_list_read(struct file *file, char __user *buf,
			      size_t count, loff_t *ppos)
{
	char *kbuf;
	int len;
	ssize_t ret;

	if (*ppos > 0)
		return 0;

	kbuf = kmalloc(4096, GFP_KERNEL);
	if (!kbuf)
		return -ENOMEM;

	len = awg_proxy_list(kbuf, 4096);

	if ((size_t)len > count)
		len = count;
	if (copy_to_user(buf, kbuf, len)) {
		kfree(kbuf);
		return -EFAULT;
	}

	*ppos += len;
	ret = len;
	kfree(kbuf);
	return ret;
}

#if LINUX_VERSION_CODE >= KERNEL_VERSION(5, 6, 0)
static const struct proc_ops proc_list_ops = {
	.proc_read = proc_list_read,
};
#else
static const struct file_operations proc_list_ops = {
	.owner = THIS_MODULE,
	.read  = proc_list_read,
};
#endif

/*
 * /proc/awg_proxy/version - read module version
 */
static ssize_t proc_version_read(struct file *file, char __user *buf,
				 size_t count, loff_t *ppos)
{
	char ver[64];
	int len;

	if (*ppos > 0)
		return 0;

	len = snprintf(ver, sizeof(ver), "%s\n", AWG_PROXY_VERSION);
	if ((size_t)len > count)
		len = count;
	if (copy_to_user(buf, ver, len))
		return -EFAULT;

	*ppos += len;
	return len;
}

#if LINUX_VERSION_CODE >= KERNEL_VERSION(5, 6, 0)
static const struct proc_ops proc_version_ops = {
	.proc_read = proc_version_read,
};
#else
static const struct file_operations proc_version_ops = {
	.owner = THIS_MODULE,
	.read  = proc_version_read,
};
#endif

/* ────────────────────── Module init/exit ─────────────────────────── */

static int __init awg_proxy_init(void)
{
	pr_info("awg_proxy: loading v%s (UDP proxy mode)\n", AWG_PROXY_VERSION);

	/* Create procfs directory */
	proc_dir = proc_mkdir("awg_proxy", NULL);
	if (!proc_dir) {
		pr_err("awg_proxy: failed to create /proc/awg_proxy\n");
		return -ENOMEM;
	}

	proc_create("add", 0220, proc_dir, &proc_add_ops);
	proc_create("del", 0220, proc_dir, &proc_del_ops);
	proc_create("list", 0444, proc_dir, &proc_list_ops);
	proc_create("version", 0444, proc_dir, &proc_version_ops);

	pr_info("awg_proxy: loaded, /proc/awg_proxy/ ready\n");
	return 0;
}

static void __exit awg_proxy_exit(void)
{
	/* Remove procfs entries */
	remove_proc_entry("add", proc_dir);
	remove_proc_entry("del", proc_dir);
	remove_proc_entry("list", proc_dir);
	remove_proc_entry("version", proc_dir);
	remove_proc_entry("awg_proxy", NULL);

	/* Stop all proxies */
	awg_proxy_cleanup();

	pr_info("awg_proxy: unloaded\n");
}

module_init(awg_proxy_init);
module_exit(awg_proxy_exit);
