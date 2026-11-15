// The board's speaker (Waveshare ESP32-S3-Touch-AMOLED-2.16: ES8311 codec and an amplifier). Only a short sound when
// the board starts: a yawn, then a chirp, in step with the face waking up (screen_wakeup). The audio driver is set up
// for it and taken down after, so it holds no internal RAM once mirroring needs it. Other builds play nothing.
#pragma once

void boot_sound_play();  // starts the sound and returns at once
