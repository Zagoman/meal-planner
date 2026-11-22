#!/bin/sh
# pg_dump loop for the mealplanner stack. Runs in a postgres:17 container
# with PGHOST/PGUSER/PGPASSWORD/PGDATABASE set (see compose.yml).
#
#   sh /backup.sh        dump now if none in the last 24 h, then daily at BACKUP_AT
#   sh /backup.sh now    one dump, then exit
#
# Dumps are custom format (pg_dump -Fc): restore with pg_restore (deploy/README.md).
set -u

dir=/backups
at=${BACKUP_AT:-03:00}
keep=${BACKUP_KEEP_DAYS:-14}

log() { echo "$(date -Iseconds) backup: $*"; }

dump() {
	ts=$(date +%Y%m%d-%H%M%S)
	tmp="$dir/.mealplanner-$ts.dump.partial"
	if pg_dump --format=custom --file="$tmp"; then
		mv "$tmp" "$dir/mealplanner-$ts.dump"
		log "wrote mealplanner-$ts.dump ($(du -h "$dir/mealplanner-$ts.dump" | cut -f1))"
	else
		rm -f "$tmp"
		log "FAILED: pg_dump exited non-zero"
		return 1
	fi
	# Keep $keep days: delete dumps older than that.
	find "$dir" -maxdepth 1 -name 'mealplanner-*.dump' -mtime +"$((keep - 1))" -print -delete |
		sed 's/^/pruned /'
}

if [ "${1:-}" = now ]; then
	dump
	exit $?
fi

if [ -z "$(find "$dir" -maxdepth 1 -name 'mealplanner-*.dump' -mmin -1440)" ]; then
	log "no dump in the last 24 h, dumping in 60 s (after the app's migrations)"
	sleep 60
	dump || true
fi

while :; do
	now=$(date +%s)
	next=$(date -d "today $at" +%s)
	[ "$next" -le "$now" ] && next=$(date -d "tomorrow $at" +%s)
	log "next dump at $(date -d "@$next" -Iseconds)"
	sleep "$((next - now))"
	dump || true
done
