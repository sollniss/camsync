# camsync

Fix the clock of a camera after the fact. `camsync` scans a directory of
photos, groups them by camera (EXIF Make/Model) and lets you shift the
timestamps of one camera by a few seconds, minutes or hours so that photos
from several cameras sort in the correct order.

**Image files are never modified.** All changes are written to XMP sidecar
files, which Lightroom, darktable, digiKam, exiftool and most other tools
read. Existing sidecars are updated in place and their other content is kept.

## Build

```sh
go build -o camsync ./cmd/camsync
```

## Usage

```
camsync list     [DIR] [-v] [-r] [-o table|json]
camsync suggest  [DIR] [--reference NAME]... [--clock-zone NAME=ZONE]... [--max-offset DURATION]
camsync shift    [DIR] --camera NAME [--by DURATION] [--timezone OFFSET] [--dry-run]
camsync localize [DIR] [--camera NAME] [--zone OFFSET] [--zone-gap DURATION] [--clock-zone NAME=ZONE]... [--dry-run] [-v]
camsync geotag   [DIR] [--camera NAME] [--gpx FILE]... [--max-gap DURATION] [--max-bridge DURATION] [--max-distance METRES] [--dry-run] [-v]
camsync reset    [DIR] --camera NAME [--dry-run]
camsync organize [DIR] [--ui] [--gap DURATION] [--jump DISTANCE] [--home LAT,LON] [--inbox DIR]... [--layout auto|trips|periods] [--plan FILE] [--apply FILE] [--dry-run]
```

1. See which cameras are in the directory:

   ```
   $ camsync list ~/Photos/2024-trip
   #  CAMERA         FILES  FIRST                LAST                 OFFSET  SIDECARS  NOTES
   1  Canon EOS R5   132    2024-05-01 08:12:03  2024-05-04 19:44:10  none    0
   2  SONY ILCE-7M3  88     2024-05-01 08:10:55  2024-05-04 19:40:01  none    0
   ```

2. Let `suggest` work out how far each camera is off (see
   [Finding the offset](#finding-the-offset)), or compare a few photos
   yourself.

3. Shift one camera. `--camera` takes the name case-insensitively, the row
   number from the list, or a unique substring of the name:

   ```
   $ camsync shift --camera sony --by 2m30s ~/Photos/2024-trip
   ```

   Use `--dry-run` (`-n`) first. Shifts accumulate: running `--by 30s` twice
   shifts by one minute. `list` shows the offset currently applied.

4. Optionally rewrite all photos in the local time of the trip with
   `localize` (see [Local time](#local-time)), and give photos without a
   location the one of photos taken at the same time with `geotag` (see
   [Locations](#locations)).

5. Undo everything for a camera with `reset`, which writes the timestamps
   from the image files back into the sidecars and removes the locations
   `geotag` wrote.

6. Tidy the whole library with `organize`, which finds the trips among all
   folders and suggests merges and moves, on the command line or in the
   browser with `--ui` (see [Organizing a library](#organizing-a-library)).

Durations: `2m30s`, `-45s`, `1h`, `+1d2h`. A leading sign applies to the whole
value. `--timezone +02:00` (or `--tz`) stamps a zone on the written dates (see Timezones). Add
`-r` to include sub-directories. Every command has `--help` with examples.

### Finding the offset

`suggest` measures every camera against the clocks that can be trusted and
prints the shift that fixes it. It writes nothing:

```
$ camsync suggest ~/Photos/2024-trip
#  CAMERA           FILES  CLOCK      CLOCK ZONE        SHIFT   CONFIDENCE  NOTES
1  Apple iPhone 15  210    reference  local             -       -           phone; GPS: clock exact
2  Canon EOS R5     132    estimated  +09:00            +10m4s  high        41 photos within 1m of a reference photo
3  SONY ILCE-7M3    88     estimated  +00:00 (guessed)  -2m     medium      12 photos within 1m of a reference photo

To apply:
  camsync shift --camera 'Canon EOS R5' --by +10m4s ~/Photos/2024-trip
  camsync shift --camera 'SONY ILCE-7M3' --by -2m ~/Photos/2024-trip
```

The reference clocks are phones, whose clocks are set from the network, and
cameras whose timestamps agree with the GPS time in their photos (within a
minute). Name them yourself with `--reference`, repeated for several, when
there is no phone or you set a camera's clock carefully.

A camera whose photos carry GPS time and a time zone is measured against GPS
time directly. Every other camera is assumed to be off by the same amount for
the whole directory, and that amount is found by lining up its shooting
activity with the reference photos: people photograph the same moments, so
the right shift makes the photos of both land close together. This needs the
cameras to have been together for at least a handful of moments and is
usually right to within a minute; photos of the very same moment make it
more precise. The confidence is high when no other shift explains the photos
nearly as well; the `±` in the notes says how far the matched photos alone
let the estimate move. Check a few photos before applying a low-confidence
suggestion.

A camera whose photos span several days is also aligned day by day. When a
day disagrees with the rest with high confidence, the clock was changed
during the trip (a reset, a DST switch, a battery pull) and the notes say
so with the offset of each such day: apply the shift to the affected photos
separately, in their own directory.

The search covers ±12 hours; `--max-offset` changes that. Much more than
half a day and the previous or next day's activity starts to look like a
match. Suggestions are relative to the effective timestamps, so a camera that
was already shifted correctly shows `none`.

Phones are recognised by their `Make` and `Model`. GPS times come from the
image files only; photos geotagged afterwards from a track log carry a GPS
time derived from the camera clock and prove nothing about it.

Photos are compared as moments in time, the way Immich orders them, so
`suggest` needs to know the zone each camera's clock runs on, shown as CLOCK
ZONE:

- the zone the camera's photos record, such as `+09:00` for a camera left on
  Japan time on a trip to Germany;
- `local` for phones, which follow the zone of wherever they are;
- the zone implied by the camera's GPS times;
- for a camera without any of these, the whole hour (or a zone seen on the
  phones) that best lines its wall clock up with the references, marked
  `(guessed)`. A zone-less camera set to UTC shows up as `+00:00`, one set to
  local time as the local zone. The clock error is what remains.

Correct a wrong guess with `--clock-zone sony=Z` (camera as for `--camera`,
any zone `--timezone` accepts). Only when no photo carries any zone
information are wall clocks compared.

### Local time

Immich shows and groups photos by the zone recorded with their date. A camera
left on Japan time puts a German evening on the next day, and zone-less
photos are shown in the wrong zone. `localize` rewrites every photo in the
local time of where it was taken, keeping the moment, so the photo order does
not change:

```
$ camsync localize --dry-run ~/Photos/2017-rügen
CAMERA                FILES  CLOCK ZONE  LOCAL ZONE  WRITTEN  SKIPPED  DAY CHANGES
Canon PowerShot G9 X  367    +09:00      +02:00      367      0        6
HTC Desire 500        343    local       +02:00      168      175      0
Sony F3111            101    local       -           0        101      0
HTC Desire 500: 175 already in local time
Sony F3111: 101 already in local time

dry run: 535 sidecars written (168 new), 276 skipped (276 already local), 0 failed; local zone from 444 phone photos
```

The local zone comes from the phones: the zone their photos record, or the
difference between their clock and their GPS time. For each photo the phone
photo closest in time decides, within `--zone-gap` (default 24h), so trips
across zones and daylight saving changes come out right. A camera's own zone
setting never counts as evidence. Where no phone photo is within reach,
`--zone +02:00` sets the local zone; otherwise the photo is skipped.

The moment each photo was taken comes from its camera's clock zone, as shown
by `suggest`; check it there first and fix a guess with `--clock-zone`.
Guesses with low confidence are not used. Run `shift` for clock errors before
`localize`, since the local zone is looked up at the moment the clock says.

`--camera` limits the rewrite to one camera; all cameras still provide
evidence. `-v` lists every photo. Photos already in local time are left
alone, so `localize` can be run again after adding photos. `reset` undoes it
together with any shift. `list` keeps showing only the shift for photos whose
image records a zone; for a zone-less image it cannot tell a zone change
from a shift, so a UTC camera localized to +02:00 shows `+2h`.

### Locations

`geotag` gives photos without a location the location of photos taken at
about the same time that have one, usually from a phone:

```
$ camsync geotag --dry-run ~/Photos/2017-rügen
CAMERA                FILES  LOCATED  WRITTEN  INTERPOLATED  SKIPPED  MEDIAN GAP  MAX GAP
Canon PowerShot G9 X  367    0        180      116           187      1m27s       13m8s
HTC Desire 500        343    169      28       18            315      3m49s       7m21s
Sony F3111            101    0        50       28            51       1m48s       7m
Canon PowerShot G9 X: 187 no located photo within 15m
HTC Desire 500: 146 no located photo within 15m
HTC Desire 500: 169 has a location
Sony F3111: 51 no located photo within 15m

dry run: 258 sidecars written (0 new), 553 skipped (169 located, 0 already geotagged), 0 failed; positions from 169 located photos
```

The located photos of each camera form a path. A photo between two photos
of one path gets the position on the line between them, by time, when both
are within `--max-gap` (default 5m), or both within `--max-bridge` (default
15m) and no more than 1 km apart: whoever photographed the same square twice
in a quarter of an hour was there in between. Otherwise the nearest located
photo within `--max-gap` is copied. No position is ever made up between the
photos of two different cameras, which may have been in different places.
When several cameras could place a photo, the estimate reaching least far in
time wins. MEDIAN GAP and MAX GAP show how far in time the written estimates
reach, MAX DIST how far apart the two photos an estimate lies between were,
which bounds how far the true position can be from the line between them;
`--max-distance 500m` refuses to interpolate beyond that and copies the
nearest photo instead. `-v` lists every photo with the photos its position
comes from.

A GPS track recorded with a watch or a tracking app places photos far more
densely than located photos can: `--gpx track.gpx`, repeated for several
files, adds its points as evidence under the same rules, so most photos
land within seconds of a track point.

A located photo's moment is its GPS time; every other photo's moment comes
from its camera's clock and clock zone, as for `localize`. Run `suggest` and
`shift` first, since a clock that is ten minutes off puts photos ten minutes
of walking away.

Positions are written as `exif:GPSLatitude` and `exif:GPSLongitude` with
`exif:GPSProcessingMethod` set to `camsync`. Photos with a location of their
own, from the image or from a sidecar written by another tool, are never
touched, and estimates are never used to place other photos. Running
`geotag` again updates estimates when the evidence changed, and `reset`
removes them while keeping locations from anywhere else.

### Organizing a library

`organize` looks at a whole library rather than one trip. It groups the
photos of every camera into events by time and place, works out what each
folder holds and suggests where photos would rather be. Nothing is moved
until a plan is applied:

```
$ camsync organize ~/Photos
HOME  52.5238467, 13.4038401 (inferred: photos on the most days; override with --home)

EVENTS
#   DATES                    KIND    PHOTOS  LOCATED  CAMERAS                                       FOLDERS
26  2024-05-10 - 2024-05-13  trip    53      13       Apple iPhone 15, Canon EOS R5, SONY ILCE-7M3  Inbox (12), Trips/Rügen Canon (20), Trips/Rügen Sony (20), 2024-05 (1)
34  2024-09-02 - 2024-09-06  trip    40      20       Apple iPhone 15, Canon EOS R5                 Inbox (40)
...

FOLDERS
FOLDER             KIND    PHOTOS  EVENTS  NOTE
2024-05            period  10      10      named after a month
Inbox              inbox   63      9       named like an inbox
Trips/Rügen Canon  event   21      2       holds one event
Trips/Rügen Sony   event   20      1       holds one event

SUGGESTIONS
ID  ACTION  PHOTOS  CONFIDENCE  FROM               TO                                   REASON
s1  move    12      medium      Inbox              Trips/Rügen Canon                    part of the event kept in Trips/Rügen Canon: ...
s2  merge   20      medium      Trips/Rügen Sony   Trips/Rügen Canon                    Trips/Rügen Sony holds the same event as Trips/Rügen Canon: ...
s3  create  40      high        Inbox              Trips/2024-09-02 - 2024-09-06 (new)  a trip without a folder, in Inbox: ...
s4  create  1       high        2024-03            2024-07 (new)                        taken outside 2024-03
s5  move    1       high        Trips/Rügen Canon  Inbox                                not part of the event kept in Trips/Rügen Canon: ...

NOTES
- Panasonic DMC-TZ100: 5 photos dated from 2010-01-01 00:05; the clock looks reset, fix the dates before sorting
```

An event ends where the pause until the next photo exceeds `--gap` (default
1d), where the location jumps further than `--jump` (default 50km) between
located photos, or where the photos go from home to away or back. Photos are
placed by their wall clock, so cameras whose clocks are hours apart still
share an event; run `suggest` and `shift` per trip afterwards. A change of
place is placed at the longest pause between the two located photos, which is
where the travelling was done, so the photos of cameras without GPS taken in
between fall on the right side; a single photo somewhere else, from a phone
that stayed at home or a stale GPS fix, is ignored.

Home is the place with photos on the most days, which holiday photos, many
but on few days, do not win. `--home LAT,LON` overrides the guess and
`--home-radius` (default 30km) says how far it reaches. An event away from
home that runs into the next day (`--min-trip`, default 18h) is a trip, a
shorter one an outing. Without any positions only time is known: an event
long enough with at least `--min-trip-photos` (default 10) photos is a trip,
and every suggestion about it has low confidence.

Folders are read from their contents and their names: an **event** folder
holds one event or a few close together, a **period** folder is named after
a year or month (`2024`, `2024-05`, `2024/05`; `2024-05 Rügen` names an
event), an **inbox** is where photos land before they are sorted (the root
itself, a folder named inbox, unsorted, import, DCIM and the like, or any
folder given with `--inbox`), and everything else is **mixed**. Suggestions
follow from the mismatches:

- **merge**: an event folder holds the same event as another one, such as a
  folder per camera for the same trip.
- **move**: photos of an event into the folder that holds it, whether they
  sit in the inbox or are strays in another event folder; a stray that
  belongs to no event kept anywhere back to the inbox; a photo out of a
  period folder it was not taken in. An event that starts inside a month and
  spills into the next stays where it is.
- **create**: a trip in the inbox that has no folder yet, named after its
  dates and placed where the other event folders are.
- **stray**: photos that do not belong where they are, with no inbox to go to.

Small events and single snaps in the inbox are left alone. Trips found inside
period folders stay there unless `--layout trips` asks for a folder per
trip; `--layout periods` never creates trip folders. Confidence is high when
positions place the event on both sides, medium when one side is placed by
time alone and low when only time says so.

`--ui` opens the same picture in the browser: a timeline of the events, a
map, the folders, and the suggestions with previews of the photos they
concern. Change the thresholds and replan without scanning again, accept
suggestions one by one or all of high confidence, and apply. Previews come
from the JPEG that raw files embed, cached under the user's cache directory.
The server listens on the loopback interface only.

```sh
camsync organize --ui --open ~/Photos
```

From the command line, `--plan plan.json` writes every suggestion with a
destination as a plan file, a JSON list of moves to review or edit;
`--apply plan.json` carries it out, `--dry-run` first. The browser's *Save
plan* button writes the same file. Photos are renamed, never copied over an
existing file, and their sidecars move with them. Catalog applications such
as Lightroom or digiKam reference files by path and lose track of moved
files; Immich rescans external libraries on its own.

The notes report what no move can fix: photos without a date, a camera
whose photos are dated from the first hours of a New Year's Day, which is
where a clock starts after a reset, and photos without camera data in event
folders, which are usually screenshots or downloads.

### Output for scripts

`--output json` (`-o json`) prints the same information as a JSON document
instead of a table, for every command:

```sh
camsync list -o json ~/Photos/trip | jq -r '.cameras[] | "\(.index) \(.camera) \(.offset)"'
```

Timestamps are ISO 8601 strings with the zone when known.

With `-o json` the document is still printed before exit code 1 when some
sidecars failed, so scripts can see which ones.

### Configuration

Persistent settings live in `$XDG_CONFIG_HOME/camsync/config.yaml`
(usually `~/.config/camsync/config.yaml`; `config.json` works too),
or in the file given with `--config`:

```yaml
sidecar-style: ext # or name
recursive: false
output: table # or json
```

Environment variables override the file (`CAMSYNC_SIDECAR_STYLE=name`,
`CAMSYNC_RECURSIVE=true`, `CAMSYNC_OUTPUT=json`), and flags override
both.

### Shell completion

The binary generates completion scripts for bash, zsh, fish and PowerShell.
Completion covers commands, flags, directories, and the values of
`--sidecar-style`, `--output` and `--camera`; the latter scans the directory
on the command line and offers the camera names and row numbers it finds.

```sh
# bash (~/.bashrc)
source <(camsync completion bash)
# zsh (~/.zshrc, before compinit)
camsync completion zsh > "${fpath[1]}/_camsync"
# fish
camsync completion fish > ~/.config/fish/completions/camsync.fish
```

`camsync completion --help` has the details for each shell.

## What gets written

The sidecar receives the shifted time as `exif:DateTimeOriginal`,
`exif:DateTimeDigitized` (if the source has one), `xmp:CreateDate` and
`photoshop:DateCreated`. New sidecars also get `tiff:Make` and `tiff:Model`.
Fractional seconds and the UTC offset (`OffsetTimeOriginal`) of the original
are preserved. `geotag` writes `exif:GPSLatitude`, `exif:GPSLongitude` and
`exif:GPSProcessingMethod` instead (see [Locations](#locations)).

Timestamps are read from the sidecar first and from the image file only when
the sidecar has none. The camera name comes from the sidecar's `tiff:Make` and
`tiff:Model` when present, otherwise from the image.

Sidecar naming: new sidecars are `IMG_0001.JPG.xmp` by default, which is
what Immich, exiftool and darktable use. `--sidecar-style name` creates
`IMG_0001.xmp` instead (Adobe, digiKam). Existing sidecars of either style
are found regardless of the flag. With the `name` style two images that
differ only by extension (`IMG_0001.JPG` and `IMG_0001.CR2`) would share
`IMG_0001.xmp`; such files are reported and skipped.

New sidecars use the same layout exiftool writes (one `rdf:Description` per
namespace, properties as child elements), so exiftool-based readers such as
Immich parse them exactly like their own.

## Timezones

Cameras record wall-clock time. Shifting never converts between zones; it
moves the wall clock and keeps the zone of the source, if any. (Converting to
the local zone of the trip is what `localize` does.) The zone comes from:

- EXIF `OffsetTimeOriginal` (EXIF 2.31, cameras from about 2019 on) or the
  `OffsetTime` / `OffsetTimeDigitized` tags when that one is missing.
- The camera's configured zone from the maker note, chosen by the `Make`
  tag: Canon's `TimeInfo` record (written since about 2009, DST included)
  and Nikon's `WorldTime` record (base zone plus a DST flag, which adds an
  hour). exiftool reports both as `TimeZone` and Immich uses them, so the
  sidecar gets the same zone Immich already shows for the image.
- The zone of an existing sidecar date.

If none of these exist, the written date has no zone, which `list -v` shows
by printing the timestamp without an offset. That matters for Immich: it
knows the zone from the image but treats a zone-less sidecar date as UTC, so
the photo would appear shifted by the zone offset. Pass `--timezone +02:00` (any
`+hh:mm`, or `Z`; `--tz` is an alias) to stamp a zone on the written dates. It can be used
without `--by` to fix the zone alone. Other makers' maker notes are not read;
use `--timezone` for those.

Phones usually store no zone at all. Immich derives one from the GPS
coordinates or the GPS clock, so it shows a zone where `list` shows none.
That is expected: a phone's clock is network-synced, so it is the reference
you align other cameras to, not something you shift.

## Immich

Immich only looks for sidecars when its sidecar job runs; the external
library scan and file watcher ignore `.xmp` files, and a rescan only
re-reads an asset when the image itself changed. After running `shift`:

Administration → Jobs → **Sidecar Metadata** → **Sync** picks up new sidecars.

## Supported formats

Detection is by file content, not extension.

| Container                           | Extensions                                                                                                    |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| JPEG                                | jpg, jpeg, jpe, jfif                                                                                          |
| TIFF and TIFF-based raw             | tif, tiff, dng, cr2, nef, nrw, arw, srf, sr2, pef, orf, rw2, rwl, srw, 3fr, fff, erf, mef, mos, kdc, dcr, iiq |
| Canon CR3                           | cr3                                                                                                           |
| Fujifilm RAF                        | raf (EXIF from the embedded JPEG)                                                                             |
| Minolta MRW                         | mrw                                                                                                           |
| PNG (`eXIf` chunk)                  | png                                                                                                           |
| WebP (`EXIF` chunk)                 | webp                                                                                                          |
| HEIF / HEIC / AVIF                  | heic, heif, hif, avif                                                                                         |
| JPEG XL (container with `Exif` box) | jxl                                                                                                           |

Files without a readable timestamp are listed but skipped when shifting.
