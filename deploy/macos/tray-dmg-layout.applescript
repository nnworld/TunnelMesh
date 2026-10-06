-- tray-dmg-layout.applescript positions the icons inside a mounted installer volume so the
-- disk image reads as an installer instead of as a folder dump.
--
-- The state it writes lives in .DS_Store and Finder owns that file, so hdiutil cannot
-- produce it on its own: the image is mounted read-write, Finder lays it out, and the laid
-- out image is then converted into the compressed read-only form that gets published.
--
-- usage: osascript tray-dmg-layout.applescript <mount-point> <app-name> [<background-png>]
--
-- The mount point and the app name are arguments because the same pass runs once per
-- architecture back to back. The geometry is not: it is a design decision, and keeping it
-- here gives the window exactly one source of truth.

on run argv
  set mountPoint to item 1 of argv
  set appName to item 2 of argv
  if (count of argv) > 2 then
    set backgroundPath to item 3 of argv
  else
    set backgroundPath to ""
  end if

  set windowWidth to 640
  set windowHeight to 420
  set iconSize to 96
  set appPosition to {170, 210}
  set applicationsPosition to {470, 210}

  tell application "Finder"
    set desktopBounds to bounds of window of desktop
    set screenWidth to (item 3 of desktopBounds) as integer
    set screenHeight to (item 4 of desktopBounds) as integer
    set windowLeft to (screenWidth - windowWidth) div 2
    set windowTop to (screenHeight - windowHeight) div 2

    set volumeRef to (POSIX file mountPoint) as alias
    open volumeRef
    set installerWindow to container window of volumeRef
    set current view of installerWindow to icon view
    set toolbar visible of installerWindow to false
    set statusbar visible of installerWindow to false
    set bounds of installerWindow to {windowLeft, windowTop, windowLeft + windowWidth, windowTop + windowHeight}
    set viewOptions to icon view options of installerWindow
    -- "not arranged" is what makes the positions stick: any arrangement re-flows the icons
    -- over them as soon as the window is drawn again.
    set arrangement of viewOptions to not arranged
    set icon size of viewOptions to iconSize
    if backgroundPath is not "" then
      set background picture of viewOptions to (POSIX file backgroundPath)
    end if
    set position of item appName of installerWindow to appPosition
    set position of item "Applications" of installerWindow to applicationsPosition
    -- Read the position back before the window closes. A Finder that accepted the commands
    -- but refused the write has to fail here, because the caller records the outcome in the
    -- release manifest and a false "true" is worse than an honest "false".
    set recordedPosition to position of item appName of installerWindow
    update volumeRef without registering applications
    delay 1
    -- Closing is what flushes the window state to .DS_Store. Detaching with the window
    -- still open can ship an image whose layout was never written.
    close installerWindow
    delay 1
  end tell
  if recordedPosition is not appPosition then
    error "Finder did not record the icon position for " & appName
  end if
end run
