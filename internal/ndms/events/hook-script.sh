#!/bin/sh
# 50-awg-manager.sh — NDMS hook forwarder for awg-manager (F571).
# Одна запись в spool и выход: ни fork'ов, ни сети. Порядок строк в файле =
# порядок исполнения хуков NDMS (очередь последовательная); демон читает
# файл через inotify. Демон не запущен / каталога нет — строка теряется, как
# раньше терялся отказанный POST: полный список на старте покрывает.
# 2>/dev/null стоит ДО >>: иначе отказ открыть файл печатается в stderr.
d=${0%/*}; d=${d%.d}
echo "type=${d##*/}&id=${id}&system_name=${system_name}&layer=${layer}&level=${level}&address=${address}" \
    2>/dev/null >> /var/run/awg-manager/ndm-hooks
exit 0
