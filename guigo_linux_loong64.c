//go:build linux && loong64

#include "guigo_linux_loong64.h"
#include <gtk/gtk.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

extern int guigo3CanClose(void);
extern void guigo3Ready(void);
extern void guigo3TitleChanged(int active, int maximized);
extern void guigo3DialogFinished(unsigned long long id, int status, char **paths, int count);

typedef void *(*manager_new_fn)(void);
typedef void *(*view_with_manager_fn)(void *);
typedef void *(*user_script_new_fn)(const char *, int, int, const char **, const char **);
typedef void (*add_script_fn)(void *, void *);
typedef void (*script_unref_fn)(void *);
typedef void (*load_uri_fn)(void *, const char *);

static GtkWidget *guigo_window;
static GtkWidget *guigo_overlay;
static GtkWidget *guigo_drag;
static GtkCssProvider *guigo_caption_style;
static GdkRectangle guigo_drag_rect;
static gpointer guigo_main_thread;

static const char *guigo_default_caption_css =
    ".guigo-controls button { border:0; border-radius:0; }";

typedef struct {
    unsigned long long id;
    int mode;
    char *title;
    char *default_path;
    char *default_name;
    char **filter_names;
    char **filter_patterns;
    int filter_count;
} GuigoDialogRequest;

static void guigo_dialog_free(gpointer data) {
    GuigoDialogRequest *request = data;
    for (int i = 0; i < request->filter_count; i++) {
        g_free(request->filter_names[i]);
        g_free(request->filter_patterns[i]);
    }
    g_free(request->filter_names);
    g_free(request->filter_patterns);
    g_free(request->title);
    g_free(request->default_path);
    g_free(request->default_name);
    g_free(request);
}

static gboolean guigo_dialog_idle(gpointer data) {
    GuigoDialogRequest *request = data;
    if (!guigo_window) {
        guigo3DialogFinished(request->id, -1, NULL, 0);
        return G_SOURCE_REMOVE;
    }
    GtkFileChooserAction action = GTK_FILE_CHOOSER_ACTION_OPEN;
    if (request->mode == 1) action = GTK_FILE_CHOOSER_ACTION_SAVE;
    if (request->mode == 2) action = GTK_FILE_CHOOSER_ACTION_SELECT_FOLDER;
    const char *title = request->title[0] ? request->title :
        request->mode == 1 ? "Save File" :
        request->mode == 2 ? "Select Folder" : "Open File";
    GtkFileChooserNative *dialog = gtk_file_chooser_native_new(
        title, GTK_WINDOW(guigo_window), action, NULL, NULL);
    if (!dialog) {
        guigo3DialogFinished(request->id, -1, NULL, 0);
        return G_SOURCE_REMOVE;
    }
    GtkFileChooser *chooser = GTK_FILE_CHOOSER(dialog);
    gtk_native_dialog_set_modal(GTK_NATIVE_DIALOG(dialog), TRUE);
    if (request->mode == 3)
        gtk_file_chooser_set_select_multiple(chooser, TRUE);
    if (request->mode == 1)
        gtk_file_chooser_set_do_overwrite_confirmation(chooser, TRUE);
    if (request->default_path[0]) {
        if (g_file_test(request->default_path, G_FILE_TEST_IS_DIR)) {
            gtk_file_chooser_set_current_folder(chooser, request->default_path);
        } else if (request->mode == 1) {
            char *parent = g_path_get_dirname(request->default_path);
            gtk_file_chooser_set_current_folder(chooser, parent);
            g_free(parent);
            if (!request->default_name[0]) {
                char *name = g_path_get_basename(request->default_path);
                gtk_file_chooser_set_current_name(chooser, name);
                g_free(name);
            }
        } else {
            gtk_file_chooser_set_filename(chooser, request->default_path);
        }
    }
    if (request->mode == 1 && request->default_name[0])
        gtk_file_chooser_set_current_name(chooser, request->default_name);
    if (request->mode != 2 && request->filter_count > 0) {
        for (int i = 0; i < request->filter_count; i++) {
            GtkFileFilter *filter = gtk_file_filter_new();
            gtk_file_filter_set_name(filter, request->filter_names[i]);
            char **patterns = g_strsplit(request->filter_patterns[i], ";", -1);
            for (int j = 0; patterns[j]; j++) {
                char *pattern = g_strstrip(patterns[j]);
                if (pattern[0]) gtk_file_filter_add_pattern(filter, pattern);
            }
            g_strfreev(patterns);
            gtk_file_chooser_add_filter(chooser, filter);
        }
        GtkFileFilter *all = gtk_file_filter_new();
        gtk_file_filter_set_name(all, "All files");
        gtk_file_filter_add_pattern(all, "*");
        gtk_file_chooser_add_filter(chooser, all);
    }
    char **paths = NULL;
    int count = 0;
    int status = 0;
    if (gtk_native_dialog_run(GTK_NATIVE_DIALOG(dialog)) == GTK_RESPONSE_ACCEPT) {
        GSList *selected = gtk_file_chooser_get_filenames(chooser);
        count = (int)g_slist_length(selected);
        if (count > 0) {
            paths = g_new0(char *, count);
            int i = 0;
            for (GSList *item = selected; item; item = item->next)
                paths[i++] = item->data;
            status = 1;
        }
        g_slist_free(selected);
    }
    guigo3DialogFinished(request->id, status, paths, count);
    for (int i = 0; i < count; i++) g_free(paths[i]);
    g_free(paths);
    g_object_unref(dialog);
    return G_SOURCE_REMOVE;
}

int guigo3DialogStart(unsigned long long id, int mode, const char *title,
                      const char *default_path, const char *default_name,
                      char **filter_names, char **filter_patterns, int filter_count) {
    GThread *main_thread = g_atomic_pointer_get(&guigo_main_thread);
    if (!main_thread || mode < 0 || mode > 3 || filter_count < 0) return 0;
    GuigoDialogRequest *request = g_new0(GuigoDialogRequest, 1);
    request->id = id;
    request->mode = mode;
    request->title = g_strdup(title);
    request->default_path = g_strdup(default_path);
    request->default_name = g_strdup(default_name);
    request->filter_count = filter_count;
    request->filter_names = g_new0(char *, filter_count);
    request->filter_patterns = g_new0(char *, filter_count);
    for (int i = 0; i < filter_count; i++) {
        request->filter_names[i] = g_strdup(filter_names[i]);
        request->filter_patterns[i] = g_strdup(filter_patterns[i]);
    }
    if (g_thread_self() == main_thread) {
        guigo_dialog_idle(request);
        guigo_dialog_free(request);
    } else if (!g_idle_add_full(G_PRIORITY_DEFAULT_IDLE, guigo_dialog_idle,
                                request, guigo_dialog_free)) {
        guigo_dialog_free(request);
        return 0;
    }
    return 1;
}

static gboolean guigo_delete(GtkWidget *widget, GdkEvent *event, gpointer data) {
    return guigo3CanClose() ? FALSE : TRUE;
}

static void guigo_destroy(GtkWidget *widget, gpointer data) {
    guigo_window = NULL;
    gtk_main_quit();
}

static void guigo_notify(GtkWidget *widget, gpointer data) {
    if (guigo_window)
        guigo3TitleChanged(gtk_window_is_active(GTK_WINDOW(guigo_window)),
                           gtk_window_is_maximized(GTK_WINDOW(guigo_window)));
}

static gboolean guigo_window_state(GtkWidget *widget, GdkEventWindowState *event, gpointer data) {
    guigo_notify(widget, data);
    return FALSE;
}

static void guigo_minimize(GtkButton *button, gpointer data) {
    gtk_window_iconify(GTK_WINDOW(guigo_window));
}

static void guigo_maximize(GtkButton *button, gpointer data) {
    if (gtk_window_is_maximized(GTK_WINDOW(guigo_window)))
        gtk_window_unmaximize(GTK_WINDOW(guigo_window));
    else
        gtk_window_maximize(GTK_WINDOW(guigo_window));
}

static void guigo_close_button(GtkButton *button, gpointer data) {
    if (guigo3CanClose())
        gtk_widget_destroy(guigo_window);
}

static gboolean guigo_drag_position(GtkOverlay *overlay, GtkWidget *widget,
                                    GdkRectangle *allocation, gpointer data) {
    if (widget != guigo_drag) return FALSE;
    *allocation = guigo_drag_rect;
    return TRUE;
}

static gboolean guigo_drag_press(GtkWidget *widget, GdkEventButton *event, gpointer data) {
    if (event->button != 1 || !guigo_window) return FALSE;
    if (event->type == GDK_2BUTTON_PRESS) {
        guigo_maximize(NULL, NULL);
        return TRUE;
    }
    gtk_window_begin_move_drag(GTK_WINDOW(guigo_window), event->button,
                               (int)event->x_root, (int)event->y_root, event->time);
    return TRUE;
}

static GtkWidget *guigo_caption_button(const char *icon, const char *fallback,
                                        GCallback callback) {
    GtkWidget *button = gtk_button_new_from_icon_name(icon, GTK_ICON_SIZE_BUTTON);
    if (!gtk_button_get_image(GTK_BUTTON(button)))
        gtk_button_set_label(GTK_BUTTON(button), fallback);
    gtk_button_set_relief(GTK_BUTTON(button), GTK_RELIEF_NONE);
    gtk_widget_set_size_request(button, 36, 36);
    g_signal_connect(button, "clicked", callback, NULL);
    return button;
}

static void guigo_titlebar(GtkWidget *view, const char *colors) {
    guigo_overlay = gtk_overlay_new();
    gtk_container_add(GTK_CONTAINER(guigo_overlay), view);
    gtk_container_add(GTK_CONTAINER(guigo_window), guigo_overlay);
    gtk_window_set_decorated(GTK_WINDOW(guigo_window), FALSE);

    guigo_drag = gtk_event_box_new();
    gtk_event_box_set_visible_window(GTK_EVENT_BOX(guigo_drag), FALSE);
    gtk_widget_add_events(guigo_drag, GDK_BUTTON_PRESS_MASK);
    gtk_widget_set_no_show_all(guigo_drag, TRUE);
    gtk_overlay_add_overlay(GTK_OVERLAY(guigo_overlay), guigo_drag);
    g_signal_connect(guigo_drag, "button-press-event", G_CALLBACK(guigo_drag_press), NULL);
    g_signal_connect(guigo_overlay, "get-child-position",
                     G_CALLBACK(guigo_drag_position), NULL);

    GtkWidget *controls = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0);
    gtk_widget_set_halign(controls, GTK_ALIGN_END);
    gtk_widget_set_valign(controls, GTK_ALIGN_START);
    GtkWidget *minimize = guigo_caption_button("window-minimize-symbolic", "−",
                                               G_CALLBACK(guigo_minimize));
    GtkWidget *maximize = guigo_caption_button("window-maximize-symbolic", "□",
                                               G_CALLBACK(guigo_maximize));
    GtkWidget *close = guigo_caption_button("window-close-symbolic", "×",
                                            G_CALLBACK(guigo_close_button));
    gtk_box_pack_start(GTK_BOX(controls), minimize, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(controls), maximize, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(controls), close, FALSE, FALSE, 0);
    gtk_overlay_add_overlay(GTK_OVERLAY(guigo_overlay), controls);
    guigo_caption_style = gtk_css_provider_new();
    const char *css = colors && colors[0] ? colors : guigo_default_caption_css;
    gtk_css_provider_load_from_data(guigo_caption_style, css, -1, NULL);
    gtk_style_context_add_class(gtk_widget_get_style_context(controls), "guigo-controls");
    gtk_style_context_add_provider_for_screen(gdk_screen_get_default(),
        GTK_STYLE_PROVIDER(guigo_caption_style), GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
}

static gboolean guigo_close_idle(gpointer data) {
    if (guigo_window) gtk_widget_destroy(guigo_window);
    return G_SOURCE_REMOVE;
}

void guigo3Close(void) { g_idle_add(guigo_close_idle, NULL); }

static gboolean guigo_activate_idle(gpointer data) {
    if (guigo_window) {
        gtk_window_deiconify(GTK_WINDOW(guigo_window));
        gtk_window_present(GTK_WINDOW(guigo_window));
    }
    return G_SOURCE_REMOVE;
}

void guigo3Activate(void) { g_idle_add(guigo_activate_idle, NULL); }

static gboolean guigo_title_idle(gpointer data) {
    if (guigo_window) gtk_window_set_title(GTK_WINDOW(guigo_window), (char *)data);
    g_free(data);
    return G_SOURCE_REMOVE;
}

void guigo3SetTitle(const char *title) { g_idle_add(guigo_title_idle, g_strdup(title)); }

static gboolean guigo_caption_idle(gpointer data) {
    if (guigo_caption_style) {
        const char *colors = (const char *)data;
        const char *css = colors && colors[0] ? colors : guigo_default_caption_css;
        gtk_css_provider_load_from_data(guigo_caption_style, css, -1, NULL);
    }
    g_free(data);
    return G_SOURCE_REMOVE;
}

void guigo3SetTitleBarColors(const char *colors) {
    g_idle_add(guigo_caption_idle, g_strdup(colors ? colors : ""));
}

typedef struct { int width, height; } GuigoSize;
static gboolean guigo_size_idle(gpointer data) {
    GuigoSize *size = data;
    if (guigo_window)
        gtk_window_resize(GTK_WINDOW(guigo_window), size->width, size->height);
    g_free(size);
    return G_SOURCE_REMOVE;
}

void guigo3SetSize(int width, int height) {
    GuigoSize *size = g_new(GuigoSize, 1);
    size->width = width;
    size->height = height;
    g_idle_add(guigo_size_idle, size);
}

typedef struct { double x, y, width, height; } GuigoDrag;
static gboolean guigo_drag_idle(gpointer data) {
    GuigoDrag *rect = data;
    if (guigo_drag && guigo_overlay) {
        if (rect->width > 0 && rect->height > 0) {
            guigo_drag_rect.x = (int)rect->x;
            guigo_drag_rect.y = (int)rect->y;
            guigo_drag_rect.width = (int)rect->width;
            guigo_drag_rect.height = (int)rect->height;
            gtk_widget_show(guigo_drag);
        } else {
            gtk_widget_hide(guigo_drag);
        }
        gtk_widget_queue_resize(guigo_overlay);
    }
    g_free(rect);
    return G_SOURCE_REMOVE;
}

void guigo3SetDrag(double x, double y, double width, double height) {
    GuigoDrag *rect = g_new(GuigoDrag, 1);
    *rect = (GuigoDrag){x, y, width, height};
    g_idle_add(guigo_drag_idle, rect);
}

int guigo3Run(const char *url, const char *script, const char *title,
             int width, int height, int min_width, int min_height,
             int content_titlebar, int maximized, const char *colors,
             const unsigned char *icon, int icon_length) {
    void *webkit = dlopen("libwebkit2gtk-4.0.so.37", RTLD_NOW | RTLD_LOCAL);
    if (!webkit) return 1;
    manager_new_fn manager_new = (manager_new_fn)dlsym(webkit, "webkit_user_content_manager_new");
    view_with_manager_fn view_new = (view_with_manager_fn)dlsym(webkit, "webkit_web_view_new_with_user_content_manager");
    user_script_new_fn script_new = (user_script_new_fn)dlsym(webkit, "webkit_user_script_new");
    add_script_fn add_script = (add_script_fn)dlsym(webkit, "webkit_user_content_manager_add_script");
    script_unref_fn script_unref = (script_unref_fn)dlsym(webkit, "webkit_user_script_unref");
    load_uri_fn load_uri = (load_uri_fn)dlsym(webkit, "webkit_web_view_load_uri");
    if (!manager_new || !view_new || !script_new || !add_script || !script_unref ||
        !load_uri || !gtk_init_check(NULL, NULL)) {
        dlclose(webkit);
        return 2;
    }
    g_atomic_pointer_set(&guigo_main_thread, g_thread_self());
    void *manager = manager_new();
    void *user_script = script_new(script, 0, 0, NULL, NULL);
    add_script(manager, user_script);
    script_unref(user_script);
    GtkWidget *view = GTK_WIDGET(view_new(manager));
    g_object_unref(manager);
    if (!view) {
        dlclose(webkit);
        return 3;
    }
    guigo_window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(guigo_window), title);
    gtk_window_set_default_size(GTK_WINDOW(guigo_window), width, height);
    gtk_widget_set_size_request(view, min_width, min_height);
    if (icon && icon_length > 0) {
        GdkPixbufLoader *loader = gdk_pixbuf_loader_new();
        if (gdk_pixbuf_loader_write(loader, icon, icon_length, NULL) &&
            gdk_pixbuf_loader_close(loader, NULL)) {
            GdkPixbuf *pixbuf = gdk_pixbuf_loader_get_pixbuf(loader);
            if (pixbuf) gtk_window_set_icon(GTK_WINDOW(guigo_window), pixbuf);
        }
        g_object_unref(loader);
    }
    if (content_titlebar) guigo_titlebar(view, colors);
    else gtk_container_add(GTK_CONTAINER(guigo_window), view);
    g_signal_connect(guigo_window, "delete-event", G_CALLBACK(guigo_delete), NULL);
    g_signal_connect(guigo_window, "destroy", G_CALLBACK(guigo_destroy), NULL);
    g_signal_connect(guigo_window, "focus-in-event", G_CALLBACK(guigo_window_state), NULL);
    g_signal_connect(guigo_window, "focus-out-event", G_CALLBACK(guigo_window_state), NULL);
    g_signal_connect(guigo_window, "window-state-event", G_CALLBACK(guigo_window_state), NULL);
    guigo3Ready();
    gtk_widget_show_all(guigo_window);
    if (guigo_drag) gtk_widget_hide(guigo_drag);
    if (maximized) gtk_window_maximize(GTK_WINDOW(guigo_window));
    guigo_notify(guigo_window, NULL);
    load_uri(view, url);
    gtk_main();
    if (guigo_caption_style) {
        gtk_style_context_remove_provider_for_screen(gdk_screen_get_default(),
            GTK_STYLE_PROVIDER(guigo_caption_style));
        g_object_unref(guigo_caption_style);
        guigo_caption_style = NULL;
    }
    g_atomic_pointer_set(&guigo_main_thread, NULL);
    guigo_drag = NULL;
    guigo_overlay = NULL;
    dlclose(webkit);
    return 0;
}
