// The board's speaker (Waveshare ESP32-S3-Touch-AMOLED-2.16: ES8311 codec and an amplifier). Only a short sound when
// the board starts: a yawn, then a chirp, in step with the face waking up (screen_wakeup). The audio driver is set up
// for it and taken down after, so it holds no internal RAM once mirroring needs it. It must also be over before the radio and AirPlay set up:
// played alongside them, it left internal RAM in pieces too small for the mirror task's 8 KB stack, and mirroring
// failed ("setup: mirror stream failed", 2026-11-15). Other builds play nothing.
#pragma once

void boot_sound_play();  // starts the sound and returns at once
bool boot_sound_busy();  // still playing (its task and audio driver still hold internal RAM)
