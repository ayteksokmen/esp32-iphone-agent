// The face on boards with a built-in display. Only the Waveshare ESP32-S3-Touch-AMOLED-2.16
// (480x480 CO5300 over QSPI) for now; build with `flash.sh --amoled216` (-DBOARD_AMOLED_216=1).
// Other builds compile none of this.
//
// Two robot eyes fill the middle of the screen: they blink, look around when nothing happens, and turn to where the
// agent is working on the phone (`look x y`). A dim line on top and dots at the bottom show the board's state.
#pragma once
#include <Arduino.h>

struct ScreenInfo {
  String fw;       // firmware version
  String usb;      // "ok" | "asleep" | "off"
  String net;      // "ap" | "sta" | "none"
  String ssid;     // hotspot name in AP mode
  String pass;     // hotspot password in AP mode
  String ip;
  bool ble = false;
  bool cli = false;
  int airplay = 0;  // AIRPLAY_IDLE / _CONNECTED / _STREAMING
  float fps = 0;
};

bool screen_begin();                    // false when the panel did not come up
void screen_show(const ScreenInfo &s);  // redraws the state lines that changed
void screen_tick();                     // moves the eyes on; call it often (loop)
void screen_look(float x, float y);     // look at this point of the phone's screen (fractions, 0-1)

// What the face shows. Work, think and sleep follow what the board does; the rest come from `face`.
enum Mood { MOOD_NEUTRAL, MOOD_WORK, MOOD_THINK, MOOD_HAPPY, MOOD_ASK, MOOD_SAD, MOOD_SLEEP };
void screen_active();                     // a command for the phone came: focused, awake
void screen_tap();                        // a click: the eyes squeeze a little
void screen_face(Mood m, uint32_t forMs);  // show m for forMs (0: until the next command for the phone)
