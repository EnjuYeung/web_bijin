#!/bin/sh
# 开启、更换或关闭登录两步验证。在 netcup 上运行（本机一条命令见 README）：
#   sh scripts/two-step.sh on    生成新密钥，扫码并输入一次动态码确认后才写入 .env
#   sh scripts/two-step.sh off   删除密钥，恢复只用密码登录（手机丢了也用它）
# 开启、更换和关闭都会换掉会话密钥，所有设备需要重新登录。
set -eu

cd "$(dirname "$0")/.."
container=${BIJIN_CONTAINER:-bijin}

[ -f .env ] || { echo "找不到 .env，请在部署目录运行。" >&2; exit 1; }
grep -q 'AUTH_TWO_STEP_SECRET' docker-compose.yml || {
	echo "docker-compose.yml 的 environment 里还没有 AUTH_TWO_STEP_SECRET，请照 docker-compose.example.yaml 补上。" >&2
	exit 1
}

case "${1:-}" in
on)
	# The secret goes to stdout only after a code from the app matches.
	secret=$(docker exec -i "$container" /app/bijin two-step) || exit 1
	want=true
	;;
off)
	secret=
	want=false
	;;
*)
	echo "用法：sh scripts/two-step.sh on|off" >&2
	exit 2
	;;
esac

tmp=$(mktemp)
grep -v '^AUTH_TWO_STEP_SECRET=' .env >"$tmp" || true
[ -z "$secret" ] || printf 'AUTH_TWO_STEP_SECRET=%s\n' "$secret" >>"$tmp"
cat "$tmp" >.env
rm -f "$tmp"

# Without a secret the session signature would match the one from before
# two-step was turned on; a new session key signs every device out.
docker compose run --rm --no-deps -T --entrypoint rm bijin -f /data/session.key
docker compose up -d --force-recreate bijin

i=0
until docker exec "$container" wget -qO- http://127.0.0.1:5001/api/login-options 2>/dev/null | grep -q "\"twoStep\":$want"; do
	i=$((i + 1))
	if [ "$i" -ge 60 ]; then
		echo "bijin 没有按预期启动，请查看：docker logs $container" >&2
		exit 1
	fi
	sleep 1
done

if [ "$want" = true ]; then
	echo "两步验证已开启。所有设备需要重新登录：用户名、密码，再加 App 上的动态码。"
else
	echo "两步验证已关闭。所有设备需要重新登录，只用用户名和密码。"
fi
