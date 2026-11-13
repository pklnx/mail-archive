package main

import "log/slog"

// moveNotice tells operators of 1.6.1, the last release under this name,
// where releases come from next (#585, part 8). serve and worker log it
// once at start; other commands stay silent, so cron jobs get no mail
// before 1.7.0 exists.
const moveNotice = "mail-archive moves to keepia Archive: from 1.7.0, releases are published as " +
	"ghcr.io/keepia/archive (repository github.com/keepia/archive); this image name gets no releases " +
	"after 1.6.1. Upgrade as usual once 1.7.0 is out: your .env, data and scripts keep working. " +
	"https://github.com/pklnx/mail-archive/releases/tag/v1.6.1"

func logMoveNotice(log *slog.Logger) {
	log.Warn(moveNotice)
}
