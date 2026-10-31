#ifndef GUIGO_LINUX_LOONG64_H
#define GUIGO_LINUX_LOONG64_H

int guigo3Run(const char *url, const char *script, const char *title,
             int width, int height, int min_width, int min_height,
             int content_titlebar, int maximized, const char *colors,
             const unsigned char *icon, int icon_length);
void guigo3Close(void);
void guigo3Activate(void);
int guigo3DialogStart(unsigned long long id, int mode, const char *title,
                      const char *default_path, const char *default_name,
                      char **filter_names, char **filter_patterns, int filter_count);
void guigo3SetTitle(const char *title);
void guigo3SetTitleBarColors(const char *colors);
void guigo3SetSize(int width, int height);
void guigo3SetDrag(double x, double y, double width, double height);
#endif
