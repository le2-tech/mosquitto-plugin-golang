#include <mosquitto.h>

/*
 * Mosquitto <-> Go bridge for the queue plugin.
 * Keeps Mosquitto entrypoints in C and forwards to Go exports.
 */

int go_mosq_plugin_version(int supported_version_count, const int *supported_versions);
int go_mosq_plugin_init(mosquitto_plugin_id_t *identifier, void **userdata,
                        struct mosquitto_opt *options, int option_count);
int go_mosq_plugin_cleanup(void *userdata, struct mosquitto_opt *options, int option_count);

/* Mosquitto 会调用这些 C 符号；这里转发到 Go 导出函数。 */
int mosquitto_plugin_version(int supported_version_count, const int *supported_versions) {
    return go_mosq_plugin_version(supported_version_count, (int*)supported_versions);
}

int mosquitto_plugin_init(mosquitto_plugin_id_t *identifier, void **userdata,
                          struct mosquitto_opt *options, int option_count) {
    return go_mosq_plugin_init(identifier, userdata, options, option_count);
}

int mosquitto_plugin_cleanup(void *userdata, struct mosquitto_opt *options, int option_count) {
    return go_mosq_plugin_cleanup(userdata, options, option_count);
}

int message_cb_c(int event, void *event_data, void *userdata);

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

/* 供 Go 注册/注销回调的封装函数。 */
int register_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb) {
    return mosquitto_callback_register(id, event, cb, NULL, NULL);
}

int unregister_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb) {
    return mosquitto_callback_unregister(id, event, cb, NULL);
}

/* Go -> Mosquitto 的日志桥接。 */
void go_mosq_log(int level, const char* msg) {
    mosquitto_log_printf(level, "%s", msg);
}
