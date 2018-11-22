#!/bin/bash

: "${HEKETI_PATH:=/var/lib/heketi}"
: "${DBBACKUP_PATH:=$HEKETI_PATH/dbbackup}"
ROTATENUM=40

if [ -d "$DBBACKUP_PATH" ] ; then
    ls "$DBBACKUP_PATH"
else
    mkdir -p "$DBBACKUP_PATH"
fi

while true ; do
    DATE="$(date  "+%Y-%m-%d-%H-%M-%S")"
    # clean old backups
    OLDBACKUPS="$( ls  -d1t $DBBACKUP_PATH/* | tail -n +$ROTATENUM)"
    if [ "$OLDBACKUPS" != "" ] ; then
        echo "$OLDBACKUPS" will be deleted
        rm -f $OLDBACKUPS
    fi
    # backup
    gzip < "$HEKETI_PATH/heketi.db" > "$DBBACKUP_PATH/heketi.db.$DATE.gz"
    sleep 43200
done
