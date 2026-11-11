// See status_screen.h. Pins and panel init follow Waveshare's 01_HelloWorld example for this board
// (github.com/waveshareteam/ESP32-S3-Touch-AMOLED-2.16, GFX Library for Arduino 1.6.4).
#include "status_screen.h"

#if BOARD_AMOLED_216
#include <Arduino_GFX_Library.h>

namespace {
const int LCD_CS = 12, LCD_SCLK = 38, LCD_D0 = 4, LCD_D1 = 5, LCD_D2 = 6, LCD_D3 = 7, LCD_RESET = 39;
const int W = 480, H = 480;
const int MARGIN = 36;      // the panel's corners are rounded
const int LINE_H = 40;      // text size 3 is 24 px high
const int LINES = 9;
const uint8_t BRIGHTNESS = 110;  // static text on AMOLED: keep it moderate

Arduino_DataBus *bus = nullptr;
Arduino_CO5300 *gfx = nullptr;
String shown[LINES];
uint16_t shownColor[LINES];

const uint16_t C_OK = RGB565(80, 220, 120), C_WAIT = RGB565(140, 140, 140), C_BAD = RGB565(240, 90, 80),
               C_TITLE = RGB565(255, 255, 255);

void line(int i, const String &text, uint16_t color, uint8_t size = 3) {
  if (shown[i] == text && shownColor[i] == color) return;
  shown[i] = text;
  shownColor[i] = color;
  int y = MARGIN + i * LINE_H;
  gfx->fillRect(0, y, W, LINE_H, RGB565_BLACK);
  gfx->setTextSize(size);
  gfx->setTextColor(color);
  gfx->setCursor(MARGIN, y + 8);
  gfx->print(text);
}
}  // namespace

bool screen_begin() {
  bus = new Arduino_ESP32QSPI(LCD_CS, LCD_SCLK, LCD_D0, LCD_D1, LCD_D2, LCD_D3);
  gfx = new Arduino_CO5300(bus, LCD_RESET, 0, W, H, 0, 0, 0, 0);
  if (!gfx->begin()) return false;
  bus->writeC8D8(0x36, 0xA0);  // memory access control, as in Waveshare's example
  gfx->fillScreen(RGB565_BLACK);
  gfx->setBrightness(BRIGHTNESS);
  gfx->setTextWrap(false);
  return true;
}

void screen_show(const ScreenInfo &s) {
  if (!gfx) return;
  static const char *const apText[] = {"waiting", "connected", "MIRRORING"};
  line(0, "HANDS  fw " + s.fw, C_TITLE);
  line(1, "USB:  " + s.usb, s.usb == "ok" ? C_OK : (s.usb == "off" ? C_BAD : C_WAIT));
  if (s.net == "ap") {
    line(2, "WiFi: hotspot", C_OK);
    line(3, "  " + s.ssid, C_TITLE);
    line(4, "  pass " + s.pass, C_TITLE);
  } else {
    line(2, "WiFi: " + s.net, s.net == "none" ? C_BAD : C_OK);
    line(3, "  " + s.ip, C_TITLE);
    line(4, "", C_TITLE);
  }
  line(5, String("BLE:  ") + (s.ble ? "connected" : "waiting"), s.ble ? C_OK : C_WAIT);
  line(6, String("CLI:  ") + (s.cli ? "connected" : "waiting"), s.cli ? C_OK : C_WAIT);
  int ap = constrain(s.airplay, 0, 2);
  line(7, String("AirPlay: ") + apText[ap], ap == 2 ? C_OK : C_WAIT);
  line(8, ap == 2 ? "  " + String(s.fps, 1) + " fps" : "", C_WAIT);
}

#else
bool screen_begin() { return false; }
void screen_show(const ScreenInfo &) {}
#endif
