#include "logbuf.h"
#include <stdarg.h>
#include "src/log/logring.h"

static const size_t RING_SIZE = 8192;
static const size_t PAGE_MAX = 400;  // escaped text per reply; under the reply cap (514) with the header
// The ring lives in PSRAM: 8 KB of internal RAM is worth more to the mirror task than log speed.
// Allocated on first use; without PSRAM, lines only go to serial.
static logring ring = {NULL, 0, 0, 0};
static portMUX_TYPE mux = portMUX_INITIALIZER_UNLOCKED;

static bool ringReady() {
  static char *volatile buf;
  static volatile bool tried;
  if (!tried) {
    tried = true;
    buf = (char *)heap_caps_malloc(RING_SIZE, MALLOC_CAP_SPIRAM);
    portENTER_CRITICAL(&mux);
    if (buf && !ring.buf) ring.buf = buf, ring.size = RING_SIZE;
    portEXIT_CRITICAL(&mux);
  }
  return ring.buf != NULL;
}

void board_log_keep(const char *line) {
  if (!ringReady()) return;
  char stamped[300];
  uint32_t ms = millis();
  snprintf(stamped, sizeof stamped, "[%lu.%03lu] %s", (unsigned long)(ms / 1000), (unsigned long)(ms % 1000), line);
  portENTER_CRITICAL(&mux);
  logring_add(&ring, stamped);
  portEXIT_CRITICAL(&mux);
}

void board_log(const char *fmt, ...) {
  char line[256];
  va_list ap;
  va_start(ap, fmt);
  vsnprintf(line, sizeof line, fmt, ap);
  va_end(ap);
  size_t n = strlen(line);
  if (n && line[n - 1] == '\n') line[--n] = 0;
  Serial.println(line);
  board_log_keep(line);
  relayLine(line);
}

String board_log_page(const String &arg) {
  static char page[PAGE_MAX + 1];
  unsigned long from = 0, max = 120;  // the default fits an app reply at the iPhone's usual MTU (185)
  sscanf(arg.c_str(), "%lu %lu", &from, &max);
  if (max < 16) max = 16;
  if (max > PAGE_MAX) max = PAGE_MAX;
  if (!ringReady()) return "ok 0 0 ";
  portENTER_CRITICAL(&mux);
  uint32_t next = logring_read(&ring, from, page, max + 1), end = ring.end;
  portEXIT_CRITICAL(&mux);
  return String("ok ") + next + " " + end + " " + page;
}
