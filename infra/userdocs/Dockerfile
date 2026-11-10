# Use minimal NGINX base image
FROM nginx:alpine

# Remove default nginx static files (optional but clean)
RUN rm -rf /usr/share/nginx/html/* && mkdir /usr/share/nginx/assets/

# Copy the built Docusaurus site from the prepared build context.
COPY build/ /usr/share/nginx/html/

# Replace default NGINX config with custom one
COPY nginx.conf /etc/nginx/conf.d/default.conf
