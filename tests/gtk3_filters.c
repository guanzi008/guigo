#include "../guigo_gtk3_linux.c"

int guigo3CanClose(void) { return 1; }
void guigo3Ready(void) {}
void guigo3TitleChanged(int active, int maximized) {}
void guigo3DialogFinished(unsigned long long id, int status, char **paths, int count) {}

static gboolean accepts(GtkFileFilter *filter, const char *name) {
    GtkFileFilterInfo info = {0};
    info.contains = GTK_FILE_FILTER_DISPLAY_NAME;
    info.display_name = name;
    return gtk_file_filter_filter(filter, &info);
}

int main(void) {
    GtkFileFilter *documents = guigo_file_filter_new("OFD / PDF", "*.ofd; *.pdf; ");
    g_object_ref_sink(documents);
    const char *files[] = {"sample.ofd", "sample.OFD", "sample.OfD", "sample.pdf",
                          "sample.PDF", "sample.PdF", "中文报告.PDF"};
    for (unsigned int i = 0; i < G_N_ELEMENTS(files); i++)
        g_assert_true(accepts(documents, files[i]));
    g_assert_false(accepts(documents, "sample.pdf.bak"));
    g_assert_false(accepts(documents, "sample.png"));
    g_assert_false(accepts(documents, "README"));
    GtkFileFilterInfo pdf = {0};
    pdf.contains = GTK_FILE_FILTER_MIME_TYPE | GTK_FILE_FILTER_DISPLAY_NAME;
    pdf.mime_type = "application/pdf";
    pdf.display_name = "document-without-extension";
    g_assert_true(gtk_file_filter_filter(documents, &pdf));
    GVariant *serialized = g_variant_ref_sink(gtk_file_filter_to_gvariant(documents));
    GtkFileFilter *restored = gtk_file_filter_new_from_gvariant(serialized);
    g_object_ref_sink(restored);
    for (unsigned int i = 0; i < G_N_ELEMENTS(files); i++)
        g_assert_true(accepts(restored, files[i]));
    g_assert_false(accepts(restored, "sample.pdf.bak"));
    g_assert_false(accepts(restored, "sample.png"));
    g_assert_true(gtk_file_filter_filter(restored, &pdf));
    g_object_unref(restored);
    g_variant_unref(serialized);
    g_object_unref(documents);

    GtkFileFilter *all = guigo_file_filter_new("All files", "*.*");
    g_object_ref_sink(all);
    g_assert_true(accepts(all, "README"));
    g_assert_true(accepts(all, "sample.PDF"));
    g_object_unref(all);

    GtkFileFilter *unknown = guigo_file_filter_new("Unknown", "*.ofdgounregistered987321");
    g_object_ref_sink(unknown);
    g_assert_true(accepts(unknown, "sample.ofdgounregistered987321"));
    GtkFileFilterInfo binary = {0};
    binary.contains = GTK_FILE_FILTER_MIME_TYPE | GTK_FILE_FILTER_DISPLAY_NAME;
    binary.mime_type = "application/octet-stream";
    binary.display_name = "unrelated.bin";
    g_assert_false(gtk_file_filter_filter(unknown, &binary));
    g_object_unref(unknown);
    return 0;
}
