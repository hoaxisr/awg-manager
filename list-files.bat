@echo off
robocopy . NULL /S /L /NJH /NJS /NS /NC /NDL /XD .git node_modules bin obj build cmd dist entware .svelte-kit prebuilt coder kmod scripts > tree.md