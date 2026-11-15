// Test build only (flash.sh --audio-spike): measures whether a voice pipeline fits next to mirroring. See
// audio_spike.cpp for the `spike` command.
#pragma once
#include <Arduino.h>

String audio_spike(const String &arg);
