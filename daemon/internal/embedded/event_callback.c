#include "event_callback.h"

#include <stddef.h>

void sshtunnel_invoke_event(sshtunnel_event_cb cb, const char* event) {
    if (cb != NULL) {
        cb(event);
    }
}
