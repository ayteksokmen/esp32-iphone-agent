// CLI link over Wi-Fi, see cli_tcp.h. Reads run on this file's task and hand each frame to cli_on_frame(), as
// NimBLE's host task does for cli_in writes; sends come from cli_link.cpp (BLE TX and video tasks), under a lock.
#include "cli_tcp.h"
#include <esp_heap_caps.h>
#include "lwip/sockets.h"
#include "lwip/netdb.h"
#include <esp_mac.h>
#include "cli_link.h"
#include "logbuf.h"
#include "netcfg.h"

const uint16_t PORT = 7202;
const int SEND_TIMEOUT_MS = 2000;
const int IDLE_TIMEOUT_MS = 6000;  // the CLI sends a keepalive every 2 s; a computer gone off Wi-Fi sends no FIN
const size_t FRAME_MAX = 600;      // largest frame from the computer (cli_link.cpp's TEXT_CAP + seal)

static int listener = -1;
static volatile int client = -1;
static SemaphoreHandle_t lock;  // client + writes (a record goes out whole)
static uint8_t *rx, *tx;        // PSRAM
static uint32_t winStart, winBytes;
static volatile uint32_t kbps;

// Cloud relay (docs/cli-protocol.md, "Cloud relay"). The board dials out, so no port has to be open at home.
const uint8_t RELAY_ATTACH = 0x03;           // board -> relay: board_id[6], plain
const int DIAL_TIMEOUT_MS = 5000;
const uint32_t RETRY_MIN_MS = 2000, RETRY_MAX_MS = 60000;
enum CloudState { CLOUD_OFF, CLOUD_DIALING, CLOUD_WAITING, CLOUD_JOINED };
static const char *cloudNames[] = {"off", "dialing", "waiting", "joined"};
static SemaphoreHandle_t cloudLock;  // cloudAddr, cloudErr
static String cloudAddr, cloudErr;   // "host:port"; the last failure
static volatile int cloudState = CLOUD_OFF;
static uint32_t retryAt = 0, retryGap = RETRY_MIN_MS;

static bool readAll(int fd, uint8_t *p, size_t n) {
  while (n) {
    int k = recv(fd, p, n, 0);
    if (k <= 0) return false;
    p += k;
    n -= k;
  }
  return true;
}

// cloud: fd is the relay link. Until the relay hands it a computer's first frame it only carries the relay's
// keepalives, and a computer on the home network (port 7202) takes precedence over it.
static void serve(int fd, bool cloud = false) {
  int one = 1;
  setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
  timeval stx = {SEND_TIMEOUT_MS / 1000, (SEND_TIMEOUT_MS % 1000) * 1000};
  setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &stx, sizeof stx);
  timeval srx = {IDLE_TIMEOUT_MS / 1000, (IDLE_TIMEOUT_MS % 1000) * 1000};
  setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &srx, sizeof srx);
  bool joined = !cloud;
  if (joined) {
    xSemaphoreTake(lock, portMAX_DELAY);
    client = fd;
    xSemaphoreGive(lock);
    board_log("cli: wifi link up");
  }
  for (;;) {
    if (!joined) {
      fd_set r;
      FD_ZERO(&r);
      FD_SET(fd, &r);
      FD_SET(listener, &r);
      timeval tv = {IDLE_TIMEOUT_MS / 1000, 0};
      if (select(max(fd, listener) + 1, &r, NULL, NULL, &tv) <= 0) break;  // the relay went quiet
      if (FD_ISSET(listener, &r)) break;  // a computer at home: serverTask accepts it
    }
    uint8_t hdr[2];
    if (!readAll(fd, hdr, 2)) break;
    size_t n = hdr[0] | hdr[1] << 8;
    if (n > FRAME_MAX || (n && !readAll(fd, rx, n))) break;
    if (!n) continue;
    if (!joined) {  // the relay joined a computer: from here the link is the CLI's, as a local one
      joined = true;
      cloudState = CLOUD_JOINED;
      retryGap = RETRY_MIN_MS;
      xSemaphoreTake(lock, portMAX_DELAY);
      client = fd;
      xSemaphoreGive(lock);
      board_log("cli: link up through the relay");
    }
    cli_on_frame(CLI_TCP_SLOT, rx, n);
  }
  if (joined) {
    xSemaphoreTake(lock, portMAX_DELAY);
    client = -1;
    xSemaphoreGive(lock);
    cli_reset(CLI_TCP_SLOT);
    board_log(cloud ? "cli: relay link down" : "cli: wifi link down");
  }
  close(fd);
}

static String getAddr() {
  xSemaphoreTake(cloudLock, portMAX_DELAY);
  String a = cloudAddr;
  xSemaphoreGive(cloudLock);
  return a;
}

static void fail(const char *why) {
  xSemaphoreTake(cloudLock, portMAX_DELAY);
  cloudErr = why;
  xSemaphoreGive(cloudLock);
}

// Opens the relay link and says which board this is; -1 on failure (cloudErr says why).
static int dialRelay(const String &addr) {
  int colon = addr.lastIndexOf(':');
  String host = addr.substring(0, colon), port = addr.substring(colon + 1);
  addrinfo hints = {}, *res = NULL;
  hints.ai_family = AF_INET;
  hints.ai_socktype = SOCK_STREAM;
  if (getaddrinfo(host.c_str(), port.c_str(), &hints, &res) != 0 || !res) {
    fail("relay name not found");
    return -1;
  }
  int fd = socket(AF_INET, SOCK_STREAM, 0);
  if (fd < 0) {
    freeaddrinfo(res);
    fail("no socket");
    return -1;
  }
  int fl = fcntl(fd, F_GETFL, 0);
  fcntl(fd, F_SETFL, fl | O_NONBLOCK);
  int k = connect(fd, res->ai_addr, res->ai_addrlen);
  freeaddrinfo(res);
  bool ok = k == 0;
  if (!ok && errno == EINPROGRESS) {
    fd_set w;
    FD_ZERO(&w);
    FD_SET(fd, &w);
    timeval tv = {DIAL_TIMEOUT_MS / 1000, 0};
    int err = 0;
    socklen_t len = sizeof err;
    ok = select(fd + 1, NULL, &w, NULL, &tv) == 1 && getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &len) == 0 && !err;
  }
  fcntl(fd, F_SETFL, fl);
  uint8_t rec[2 + 7] = {7, 0, RELAY_ATTACH};
  esp_read_mac(rec + 3, ESP_MAC_BT);  // the board id, as in WELCOME
  if (!ok || send(fd, rec, sizeof rec, 0) != (int)sizeof rec) {
    close(fd);
    fail("relay not reachable");
    return -1;
  }
  fail("");
  return fd;
}

// Keeps the relay link up while the board is on a Wi-Fi with internet and nobody uses port 7202.
static void cloudStep() {
  String addr = getAddr();
  if (!addr.length()) {
    cloudState = CLOUD_OFF;
    return;
  }
  if (strcmp(net_mode(), "sta") || !net_up()) {  // the hotspot has no way out
    cloudState = CLOUD_DIALING;
    return;
  }
  if ((int32_t)(millis() - retryAt) < 0) return;
  cloudState = CLOUD_DIALING;
  int fd = dialRelay(addr);
  bool joined = false;
  if (fd >= 0) {
    cloudState = CLOUD_WAITING;
    serve(fd, true);
    joined = cloudState == CLOUD_JOINED;
  }
  cloudState = CLOUD_DIALING;
  if (joined) {  // after a computer's session the board dials again at once, for the next one
    retryAt = millis();
    retryGap = RETRY_MIN_MS;
  } else if (fd >= 0 && retryGap == RETRY_MIN_MS) {  // a relay link that went quiet: once more soon
    retryAt = millis() + RETRY_MIN_MS;
    retryGap *= 2;
  } else {  // the relay can't be reached: back off up to a minute
    retryAt = millis() + retryGap;
    retryGap = min(retryGap * 2, RETRY_MAX_MS);
  }
}

static void serverTask(void *) {
  for (;;) {
    fd_set r;
    FD_ZERO(&r);
    FD_SET(listener, &r);
    timeval tv = {1, 0};
    if (select(listener + 1, &r, NULL, NULL, &tv) == 1) {
      int fd = accept(listener, NULL, NULL);
      if (fd >= 0) serve(fd);  // one computer at a time; a new one waits until this one is gone
      continue;
    }
    cloudStep();
  }
}

bool cli_tcp_begin() {
  lock = xSemaphoreCreateMutex();
  cloudLock = xSemaphoreCreateMutex();
  cloudAddr = cfg_cloud();
  rx = (uint8_t *)heap_caps_malloc(FRAME_MAX + 2 + CLI_TCP_MTU, MALLOC_CAP_SPIRAM);
  if (!rx) rx = (uint8_t *)malloc(FRAME_MAX + 2 + CLI_TCP_MTU);
  if (!rx) return false;
  tx = rx + FRAME_MAX;
  listener = socket(AF_INET, SOCK_STREAM, 0);
  if (listener < 0) return false;
  int one = 1;
  setsockopt(listener, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
  sockaddr_in addr = {};
  addr.sin_family = AF_INET;
  addr.sin_port = htons(PORT);
  addr.sin_addr.s_addr = htonl(INADDR_ANY);
  if (bind(listener, (sockaddr *)&addr, sizeof addr) < 0 || listen(listener, 1) < 0) return false;
  return xTaskCreatePinnedToCore(serverTask, "cli_tcp", 4096, NULL, 3, NULL, 1) == pdPASS;
}

bool cli_tcp_up() { return client >= 0; }

bool cli_tcp_send(const uint8_t *p, size_t n, uint32_t) {
  if (n > CLI_TCP_MTU) return false;
  xSemaphoreTake(lock, portMAX_DELAY);
  bool ok = client >= 0;
  if (ok) {
    tx[0] = (uint8_t)n;  // length and frame in one send: one segment per record
    tx[1] = (uint8_t)(n >> 8);
    memcpy(tx + 2, p, n);
    for (size_t off = 0; ok && off < n + 2;) {
      int k = send(client, tx + off, n + 2 - off, 0);
      ok = k > 0;
      off += ok ? k : 0;
    }
    if (!ok) shutdown(client, SHUT_RDWR);  // stalled (SO_SNDTIMEO) or gone: the reader sees it and closes
  }
  xSemaphoreGive(lock);
  if (ok) {
    uint32_t now = millis();
    winBytes += n + 2;
    if (now - winStart >= 1000) {
      kbps = winBytes * 8 / (now - winStart);
      winBytes = 0;
      winStart = now;
    }
  }
  return ok;
}

void cli_tcp_drop() {
  xSemaphoreTake(lock, portMAX_DELAY);
  if (client >= 0) shutdown(client, SHUT_RDWR);
  xSemaphoreGive(lock);
}

uint32_t cli_tcp_kbps() { return millis() - winStart > 2000 ? 0 : kbps; }

String cli_tcp_cloud_set(const String &hostPort) {
  String a = hostPort;
  a.trim();
  if (a.length()) {
    int colon = a.lastIndexOf(':');
    long port = colon > 0 ? a.substring(colon + 1).toInt() : 0;
    if (colon <= 0 || port <= 0 || port > 65535 || a.length() > 100) return "usage: cloud HOST:PORT | off";
  }
  xSemaphoreTake(cloudLock, portMAX_DELAY);
  cloudAddr = a;
  cloudErr = "";
  xSemaphoreGive(cloudLock);
  retryAt = 0;
  retryGap = RETRY_MIN_MS;
  cfg_set_cloud(a);
  return "";
}

String cli_tcp_cloud_json() {
  xSemaphoreTake(cloudLock, portMAX_DELAY);
  String a = cloudAddr, e = cloudErr;
  xSemaphoreGive(cloudLock);
  int st = a.length() ? cloudState : CLOUD_OFF;
  // a computer at home holds the link (port 7202): the board dials the relay once it is gone
  const char *name = a.length() && st == CLOUD_OFF && client >= 0 ? "local" : cloudNames[st];
  String j = "{\"relay\":\"" + a + "\",\"state\":\"" + name + "\"";
  if (e.length() && st == CLOUD_DIALING) j += ",\"err\":\"" + e + "\"";
  return j + "}";
}

bool cli_tcp_cloud_joined() { return cloudState == CLOUD_JOINED && client >= 0; }
