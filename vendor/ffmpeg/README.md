FFmpeg / FFprobe are the media executables used by the local Codex video
segmenter. Chain B invokes them inside its AI media-tool call.

Windows binaries are bundled in windows-x64/ (9.0.2 essentials, gyan.dev).
LICENSE.GPLv3 and UPSTREAM-README.txt accompany the binaries and include the
upstream source reference. The binaries are excluded from Git and included in
the Windows portable package. Other platforms resolve ffmpeg and ffprobe on PATH.

The plugin's AI chooses cut points from the user's B segmentation rules. The
tool aligns these to actual source frames, exports H.264/AAC segments, validates
duration and audio, and verifies complete source-frame coverage with FFprobe.
No fixed creative rules or Seedance duration rules are imported from the skill.
