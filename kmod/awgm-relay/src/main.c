// SPDX-License-Identifier: GPL-2.0
/*
 * awgm_relay — kernel-релей обфускатора для NativeWG (спека
 * docs/superpowers/specs/2026-09-26-kernel-obfuscator-relay-design.md).
 * Управление: /proc/awgm_relay/{add,del,list,version}.
 */
#include <linux/module.h>
#include <linux/kernel.h>
#include <linux/init.h>
#include <linux/slab.h>
#include <linux/proc_fs.h>
#include <linux/uaccess.h>

#include "relay.h"

#ifndef AWGM_RELAY_VERSION
#define AWGM_RELAY_VERSION "dev"
#endif

MODULE_LICENSE("GPL");
MODULE_AUTHOR("hoaxisr");
MODULE_DESCRIPTION("awgm_relay - kernel relay for obfuscated NativeWG tunnels");
MODULE_VERSION(AWGM_RELAY_VERSION);

static struct proc_dir_entry *proc_dir;

/* Строку add не логируем: в ней ключ (§3.3). */
static ssize_t proc_add_write(struct file *f, const char __user *ubuf,
			      size_t count, loff_t *ppos)
{
	char *kbuf;
	int ret;

	if (count >= AWGMR_LINE_MAX)
		return -EINVAL;
	kbuf = kmalloc(count + 1, GFP_KERNEL);
	if (!kbuf)
		return -ENOMEM;
	if (copy_from_user(kbuf, ubuf, count)) {
		kfree(kbuf);
		return -EFAULT;
	}
	kbuf[count] = '\0';
	if (count && kbuf[count - 1] == '\n')
		kbuf[count - 1] = '\0';
	ret = awgmr_relay_add(kbuf);
	memzero_explicit(kbuf, count);
	kfree(kbuf);
	return ret ? ret : count;
}

static ssize_t proc_del_write(struct file *f, const char __user *ubuf,
			      size_t count, loff_t *ppos)
{
	char kbuf[32];
	u16 port;
	int ret;

	if (count >= sizeof(kbuf))
		return -EINVAL;
	if (copy_from_user(kbuf, ubuf, count))
		return -EFAULT;
	kbuf[count] = '\0';
	if (count && kbuf[count - 1] == '\n')
		kbuf[count - 1] = '\0';
	if (awgmr_parse_listen(kbuf, &port))
		return -EINVAL;
	ret = awgmr_relay_del(port);
	return ret ? ret : count;
}

/* Позиционное чтение, как у awg-proxy (issue #362: os.ReadFile читает с 512). */
static ssize_t proc_list_read(struct file *f, char __user *ubuf,
			      size_t count, loff_t *ppos)
{
	char *kbuf = kmalloc(8192, GFP_KERNEL);
	ssize_t ret;
	int len;

	if (!kbuf)
		return -ENOMEM;
	len = awgmr_relay_list(kbuf, 8192);
	if (*ppos >= len) {
		ret = 0;
		goto out;
	}
	if (count > (size_t)len - *ppos)
		count = (size_t)len - *ppos;
	if (copy_to_user(ubuf, kbuf + *ppos, count)) {
		ret = -EFAULT;
		goto out;
	}
	*ppos += count;
	ret = count;
out:
	kfree(kbuf);
	return ret;
}

static ssize_t proc_version_read(struct file *f, char __user *ubuf,
				 size_t count, loff_t *ppos)
{
	char ver[64];
	int len;

	if (*ppos > 0)
		return 0;
	len = snprintf(ver, sizeof(ver), "%s\n", AWGM_RELAY_VERSION);
	if ((size_t)len > count)
		len = count;
	if (copy_to_user(ubuf, ver, len))
		return -EFAULT;
	*ppos += len;
	return len;
}

static const struct file_operations add_ops = { .owner = THIS_MODULE, .write = proc_add_write };
static const struct file_operations del_ops = { .owner = THIS_MODULE, .write = proc_del_write };
static const struct file_operations list_ops = { .owner = THIS_MODULE, .read = proc_list_read };
static const struct file_operations version_ops = { .owner = THIS_MODULE, .read = proc_version_read };

static int __init awgmr_init(void)
{
	int ret;

	awgmr_crc8_init();
	proc_dir = proc_mkdir("awgm_relay", NULL);
	if (!proc_dir)
		return -ENOMEM;
	ret = awgmr_xmit_dev_create();
	if (ret) {
		remove_proc_entry("awgm_relay", NULL);
		return ret;
	}
	proc_create("add", 0200, proc_dir, &add_ops);
	proc_create("del", 0200, proc_dir, &del_ops);
	proc_create("list", 0444, proc_dir, &list_ops);
	proc_create("version", 0444, proc_dir, &version_ops);
	pr_info("awgm_relay: v%s loaded\n", AWGM_RELAY_VERSION);
	return 0;
}

static void __exit awgmr_exit(void)
{
	remove_proc_entry("add", proc_dir);
	remove_proc_entry("del", proc_dir);
	remove_proc_entry("list", proc_dir);
	remove_proc_entry("version", proc_dir);
	remove_proc_entry("awgm_relay", NULL);
	awgmr_relay_cleanup();
	awgmr_xmit_dev_destroy();
	pr_info("awgm_relay: unloaded\n");
}

module_init(awgmr_init);
module_exit(awgmr_exit);
